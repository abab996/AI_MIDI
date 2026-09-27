# MelodyMate 引擎 IPC 协议（v3）

> 版本：3 ｜ 日期：2026-08-23 ｜ 修订：2026-09-27（P4 FX 链与主总线 §4.13；P5 VST3 宿主 §4.14；线格式不变）
> 适用双方：`MelodyMate.exe`（Go 主进程，客户端）↔ `melodymate-engine.exe`（JUCE 引擎，服务端）
> 实现镜像：Go 侧 `internal/engine/protocol.go`；C++ 侧 `engine/Source/Ipc/Protocol.h`
> **本文为协议唯一权威来源**，两侧实现与 engine/README 摘要如有出入，以本文为准。
>
> 版本策略：握手双方**严格比对**版本号（`hello` 的 `protocolVersion` 必须等于对方常量）。
> 主程序与引擎同包分发，版本不一致时旧引擎会被明确拒绝（`protocol version mismatch`），
> 而不是以旧行为继续跑。

---

## 1. 管道命名与连接规则

- 管道名：`\\.\pipe\MelodyMate_ENGINE_<引擎进程PID>`
- PID 为**引擎自身**的进程 PID。主进程经 `os/exec` 启动引擎后从 `cmd.Process.Pid` 获得该值并拼接连接；
- 单实例单客户端：引擎以 `PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE | PIPE_REJECT_REMOTE_CLIENTS`
  创建字节模式管道，仅允许主进程连接；名字被占用（残留实例/抢占）时每 500ms 重试；
- 断开重连：客户端断开后引擎回到监听状态等待重连，不退出；**重连后握手状态复位，必须重新 hello**。

## 2. 帧格式

```
[uint32 LE 长度 n][n 字节 payload]        n ≤ 16 MiB（kMaxFrameSize，收发两侧均校验）
payload = [1 字节消息类型][消息体]
```

| 类型 | 值 | 方向 | 消息体 |
|---|---|---|---|
| Request | 0x01 | Go → Engine | UTF-8 JSON：`{"id":<num>,"method":"...","params":{...}}` |
| Response | 0x02 | Engine → Go | UTF-8 JSON：`{"id":<num>,"ok":true,"result":...}` 或 `{"id":<num>,"ok":false,"error":{"message":"..."}}` |
| Event | 0x03 | Engine → Go | UTF-8 JSON：`{"event":"...","data":{...}}` |
| Midi | 0x04 | Go → Engine | 二进制 MIDI 消息，3/4 字节双版本（见下） |
| Timecode | 0x05 | Engine → Go | 二进制定长 25 字节走带时间码（M3，见下） |

**Midi 帧（0x04）布局**：payload 在类型字节后为一条 MIDI 消息，**两个版本并存**——
- 3 字节（旧单轨）：`[status][data1][data2]`，等价 track 0；
- 4 字节（多轨，每轨独立 SF2）：`[track][status][data1][data2]`，track 0–31。

status 为完整状态字节（`0x90|ch` noteOn、`0x80|ch` noteOff），
data1 为键号、data2 为力度；noteOn 且 data2=0 按 MIDI 约定等价 noteOff。
引擎按 payload 长度区分版本（3 字节 → track 0，4 字节 → 取首字节为 track）。

**Timecode 帧（0x05）布局**（M3）：payload 在类型字节后为 25 字节——
`[samplePos int64 LE][beatPos float64 LE][bpm float64 LE][playing uint8]`。
播放中 50Hz 周期推送；stop 时推送终态一帧。客户端也可用 `timecode` 方法拉取同源数据。

JSON 约定：
- 编码 UTF-8，无 BOM；**载荷不含结尾 NUL 字节**（C++ 侧注意 `CharPointer_UTF8::sizeInBytes()` 含终止符，发送时须 -1）；
- 控制类消息一律走 JSON；实时高频消息（Midi/Timecode）必须走预留的二进制类型，禁止 JSON 化；
- 长度为 0 的帧视为传输错误，接收方断开连接。

## 3. 会话状态机

```
[启动] → 监听 → 客户端连接 → 未握手
未握手：仅放行 hello；其余请求返回 ok=false "engine not ready: send hello first"
hello 成功 → 已握手：全部方法可用
连接断开 → 回到监听（状态复位），重连后须重新 hello
shutdown → 回复后进程优雅退出
```

版本协商：Request 的 hello params **可携带** `"protocolVersion": <int>`；
引擎校验：缺失视为接受当前版本；不匹配则拒绝握手（ok=false, message 含双方版本号）。
响应侧 `result.protocolVersion` 总是返回引擎实际版本。

## 4. 方法字典（v1）

### 4.1 `hello`
| | |
|---|---|
| params | `{}` 或 `{"protocolVersion":1}` |
| result | `{"name":"melodymate-engine","version":"0.1.0","protocolVersion":1,"capabilities":["devices","testTone"]}` |
| 错误 | 版本不匹配 |

### 4.2 `ping`
| | |
|---|---|
| params | `{}` |
| result | `"pong"` |

### 4.3 `listDevices`
枚举全部驱动类型的输出设备。**只做扫描与名称枚举，不打开任何设备**
（打开任意物理设备可能长时间阻塞；采样率/通道数留待 M4 经 applySetup 后的
summary 与扩展方法提供）。
| | |
|---|---|
| params | `{}` |
| result | `{"drivers":[{"driver":"ASIO","devices":[{"name":"..."}]}]}` |
说明：`driver` 取值即 JUCE 设备类型名（如 `ASIO`、`Windows Audio`、`DirectSound`），
applySetup 的 driver 参数须与之精确匹配；此方法在 JUCE 消息线程执行，耗时可能达数秒。

### 4.4 `applySetup`
切换设备/采样率/缓冲区；成功后附带推送 `deviceChanged` 事件。
| | |
|---|---|
| params | `{"driver?":"ASIO","device?":"...","sampleRate?":48000,"bufferSize?":128}`（均可省略，省略项保持现状） |
| result | `{"summary":"<设备> @ <采样率>Hz, buffer=<N>"}` |
| event | `{"event":"deviceChanged","data":{"summary":"..."}}` |
| 错误 | JUCE setAudioDeviceSetup 返回的错误文本 |

### 4.5 `testTone`
三角波测试音开关（M1 验收用），带电平渐变防咔哒声。
| | |
|---|---|
| params | `{"on":true,"freq?":440.0}`（freq 合法域 20–20000 Hz，越界保持原值） |
| result | `{"on":true}` |

### 4.5a `panic`
全音符停止（卡音逃生口）：丢 note-off、音色热切换、后端切换都可能留下
持续发声的音符，此方法让全部轨道按自然 release 停音（每轨对 16 通道发
`note_off_all`，经事件环投递）。
| | |
|---|---|
| params | `{}` |
| result | `{}` |
| 说明 | 管道线程直接执行（与实时音符同一生产者，保持事件环单生产者纪律）；前端入口为 `EngineBridge.panic()` / `POST /api/audio/panic` |

### 4.6 `loadSoundFont`（M2）
| | |
|---|---|
| params | `{"path":"D:/.../xxx.sf2","track?":0}`（track 缺省为 0；0–31，每轨独立 tsf） |
| result | `{"loaded":true,"path":"...","track":0}`；失败时 `loaded=false` 且附 `error` 文本（加载失败不算协议错误） |
| 说明 | 该方法在 JUCE 消息线程执行；SF2 文件可达数十 MB，首次解析耗时可达数秒 |

### 4.7 `setTrackMix`（M3 混音图）
| | |
|---|---|
| params | `{"track":0,"gain":1.0,"pan":0.0,"mute":false,"solo":false,"active":true}`（track 0–31） |
| result | `{}` |

### 4.6a `setTrackVoice`（内置波形声部）
| | |
|---|---|
| params | `{"track":0,"wave":"sawtooth","attack":0.01,"decay":0.15,"sustain":0.6,"release":0.25,"cutoff":8000,"resonance":1.0,"gain":0.7}`（track 0–31；wave ∈ sine/triangle/square/sawtooth；attack/decay/release 单位秒，sustain 0–1，cutoff Hz，gain 总增益） |
| result | `{}`；track 越界返回错误文本 |
| 说明 | 在 JUCE 消息线程执行。把该轨切到**内置波形声部**（PolyBLEP 振荡器 + 指数 ADSR + 每声部 lowpass，参数语义与前端 WebAudio SynthEngine 一致），**不依赖 SF2**——合成波音色在音频引擎模式下的原生渲染路径。与 `loadSoundFont` 互斥：任一成功都会撤下另一模式的声源（正响音符随之停止）。钢琴窗/实时键盘约定使用 track 31（`EngineBridge.PERF_TRACK`）专用演奏轨，编曲 synth 轨使用自身 idx；supervisor 会记录每轨声部参数并在会话重启后重放 |

### 4.6b `setTrackPreset`（SF2 预设选择）
| | |
|---|---|
| params | `{"track":0,"bank":0,"program":0}`（track 0–31；bank/program 为 General MIDI 编号） |
| result | `{}`；预设不存在或该轨未加载 SF2 时返回错误文本（`ok=false`） |
| 说明 | 选择已加载 SF2 音色库的预设（`tsf_channel_set_bank_preset`，channel 0）。多预设 SF2 与内置钢琴/弦乐映射（GeneralUser GS (0,0)/(0,48)）都依赖它。**不切换声源模式**（仅换预设，SF2 继续发声）；与 `loadSoundFont` 共用 loadMutex_ 互斥。钢琴窗/编曲窗在 `loadSoundFont` 成功后调用；supervisor 记录每轨 bank/program 并在会话重启后按 `loadSoundFont → setTrackPreset → setTrackVoice` 顺序重放（loadSoundFont 会重置预设状态，顺序颠倒会被默认 (0,0) 覆盖） |

### 4.6c `click`（节拍器木鱼音）
| | |
|---|---|
| params | `{"track":31,"high":true}`（high=true 重拍 1600Hz / false 弱拍 900Hz） |
| result | `{}`；track 越界静默忽略 |
| 说明 | 引擎侧合成：恒定正弦 + 起音即峰值、约 40ms 指数释放的短促包络，**独立于该轨 SF2/波形模式**（click 声部与主声源叠加渲染，不打断正响音符）。经事件环即时投递（与实时音符同一生产者纪律），无 when 参数——前端按自身调度器到点调用，IPC 往返（毫秒级）对拍间隔（≥150ms）可忽略。钢琴窗经 `EngineBridge.PERF_TRACK`(31)、编曲窗经试听轨(29) 发送 |

### 4.7a 素材调度（M3，编曲窗音频 Clip 与离线渲染共用）
| 方法 | params | result | 说明 |
|---|---|---|---|
| `scheduleSamples` | `{"clips":[{...}],"bpm":<double>}` | `{}` | 全量替换采样调度表。clip 字段：`track`(0–31)、`path`(素材绝对路径)、`start`(拍)、`length`(拍)、`offset`(秒，素材内偏移)、`fadeIn`/`fadeOut`(秒)、`gain`(线性系数)。**注意单位混合**：start/length 为拍，offset/fade 为秒。`path` 重复时仅首次解码（SamplePool 缓存）；解码在消息线程进行 |
| `clearSamples` | `{}` | `{}` | 清空采样调度表 |
| `scheduleNotes` | `{"notes":[{...}],"bpm":<double>}` | `{}` | 全量替换 MIDI 调度表。note 字段：`track`、`key`(0–127)、`vel`(1–127)、`start`(拍)、`end`(拍) |
| `clearNotes` | `{}` | `{}` | 清空 MIDI 调度表 |

### 4.7b `bounce`（M3 离线渲染）
| | |
|---|---|
| params | `{"path":"<输出 WAV 绝对路径>","bpm":<double>,"beats":<double>,"tailSec":<秒>,"sampleRate?":48000,"notes?":[...],"clips?":[...]}`；notes/clips 结构同 4.7a。缺省 `sampleRate` 时沿用当前设备采样率 |
| result | `{"path":"...","ok":true}`；主进程以返回的 path 经自身下载通道提供给前端 |
| 说明 | **消息线程异步渲染**（`MessageManager::callAsync`，v3.0.3 起；此前在管道线程同步执行会瘫痪渲染期间的整个 IPC 通道）。长曲可达分钟级——Go 侧按清单/素材展开动态估算超时（下限 30s、上限 20min，见 supervisor `estimateBounceTimeout`）；渲染期间实时音频回调静音（协作握手保证互斥） |

### 4.7c `getLevels`
| | |
|---|---|
| params | `{}` |
| result | `{"levels":[<float>, ...],"master":<float>}`（32 轨峰值电平 + 主总线峰值，压限后取值） |
| 说明 | v2 起增加 `master` 字段（P4 混音台主通道表）。读方需容忍字段缺失（旧引擎不会出现，因为握手已挡） |

### 4.7d `setLoop`（走带循环）
| | |
|---|---|
| params | `{"on":true,"start":<拍>,"end":<拍>}` |
| result | `{}` |

### 4.13 FX 链与主总线（P4）

信号链（P4 起）：`每轨 synth → 轨插入链 → 轨条(gain/pan) → 汇总 → 主插入链 → 压限 → 输出`。
每条链 8 个槽（`kMaxFxSlots`）；槽位状态 = 处理器 + 旁通 + 干湿比。
**P4 只落基础设施与槽参数**：`plugin` 非空会被拒绝（装载在 P5 VST3 / P6 MM 开放），
空槽在音频线程直接跳过（等价直通），槽参数变更即时生效。

#### 4.13.1 `fx.setChain`
| | |
|---|---|
| params | `{"track":<0-31>, "slots":[{"slot":<0-7>,"plugin":"<ref>?","bypass":<bool>,"wet":<0-1>,"off":<bool>?}]}` 或 `{"master":true,"slots":[...]}` |
| result | `{"master":<bool>,"track":<int>?,"slots":[{"slot","loaded","bypass","wet"}×8],"active":<int>}` |
| 错误 | 槽位越界 / slots 非数组 / 轨越界 / `plugin` 非空（`插件装载将在 P5（VST3）/ P6（MM）开放…`） |
| 说明 | 逐槽应用；`off=true` 显式卸载该槽已有处理器（进延迟释放队列）。被替换/卸载的处理器**不在消息线程立即 delete**——音频回调可能仍在上一块持有指针，改由 `EngineAudioDevice` 的 500ms 定时器回收 |

#### 4.13.2 `fx.getChain`
| | |
|---|---|
| params | `{"track":<0-31>}` 或 `{"master":true}` |
| result | 同 `fx.setChain` 的 result（`loaded` 反映该槽是否挂了处理器） |

#### 4.13.3 `mixer.setMaster` / `mixer.get`
| 方法 | params | result | 说明 |
|---|---|---|---|
| `mixer.setMaster` | `{"gain":<0.0-4.0>}` | `{"master":<gain>,"masterPeak":<float>,"slotsPerChain":8}` | 主总线增益；越界报错（`gain 超出范围（0.0 ~ 4.0）`） |
| `mixer.get` | `{}` | 同 `mixer.setMaster` 的 result | 只读回读（诊断/测试） |

### 4.8 `openControlPanel`
打开当前声卡的驱动控制面板（仅部分 ASIO 驱动支持）。
| | |
|---|---|
| params | `{}` |
| result | `{"opened":true}`；不支持时 `opened=false`（非错误） |
| 说明 | `opened=true` 仅表示「已请求打开面板」（立即应答）；面板实际由引擎独立后台线程打开（含 COM 初始化），**本请求不再被驱动的模态循环阻塞**——此前 showControlPanel 阻塞消息线程会把管道事务与心跳一并拖住，30s 后被主进程判死杀引擎（面板改动丢失）。同一时刻只允许一个面板，重复请求会被忽略。驱动面板内改缓冲区由 JUCE resetRequest 在面板关闭后约 500ms 自动重建设备生效 |

### 4.9 `currentSummary`
当前音频设备摘要（与 applySetup result.summary 同格式），供状态轮询。
| | |
|---|---|
| params | `{}` |
| result | `{"summary":"<设备> @ <采样率>Hz, buffer=<N>"}` |

### 4.10 走带（M3）
| 方法 | params | result | 说明 |
|---|---|---|---|
| `play` | `{}` | `{}` | 开始推进走带位置，50Hz 推送 timecode 帧 |
| `stop` | `{}` | `{}` | 停止推进，推送终态一帧 |
| `locate` | `{"beat": <double>}` | `{}` | 定位到指定拍 |
| `setTempo` | `{"bpm": <double>}` | `{}` | 变速（保持当前位置拍值，重算采样位置） |
| `timecode` | `{}` | `{"samplePos":..,"beat":..,"bpm":..,"playing":..}` | 拉取式时间码（与推送帧同源） |

### 4.11 `shutdown`

| | |
|---|---|
| params | `{}` |
| result | `"bye"`；引擎随后退出（主进程可等待进程结束或超时强杀） |

### 4.12 未知方法
返回 `ok=false`，`error.message = "unknown method: <method>"`。

## 5. 事件（Engine → Go，单向推送）

| 事件 | data | 触发 |
|---|---|---|
| `deviceChanged` | `{"summary":"..."}` | **暂未实现**（当前引擎不推送；设备摘要经 applySetup result 与 `currentSummary` 轮询获取） |

（v2 预留：`deviceListChanged` 热插拔、`xrun`、电平表、错误上报）

## 6. 超时与其他约定

- 主进程对每个请求应设超时（实测默认：Request 30s、applySetup 60s（此前 15s 会把 ASIO Link Pro 等 10–20s+ 的慢首开误判为卡死）、loadSoundFont 30s、setTrackVoice/setTrackPreset 10s、握手 20s；ping 走 TryPing 5s；click 走 Request 默认 30s）。**bounce 例外**：按渲染时长动态估算（音频时长×2 + 60s，下限 30s、上限 20min——固定 30s 会把长工程导出误判为会话失效并杀掉渲染中的引擎，v3.0.3 修复）；
- 引擎对畸形 JSON 不回复（请求方靠超时兜底），后续版本可在 Response 中引入显式 parse error；
- 心跳由**主进程侧**负责：TryPing 每 2s 一次（会话事务忙时跳过），连续失败 15 次（最坏 30–105s）判会话失效重建——阈值刻意宽松以容忍 ASIO 慢驱动首开（10–20s）；真崩溃由进程退出通道秒级检测，不走心跳；
- 本协议不含鉴权。本地信任边界收紧（客户端 PID 校验/DACL）列入 M5 前加固项。

## 修订记录

- **2026-09-27（P5：VST3 宿主，协议 v2 → v3）**：新增 plugin.* 一族（§4.14：扫描/清单/装载/卸载/参数/编辑器/状态），能力表加 st3。引擎新增 Source/Plugins/（PluginHost / Vst3FxProcessor / PluginWindow），CMake 启用 JUCE_PLUGINHOST_VST3。**同时修掉一个长期存在的编码缺陷**：JUCE 的 String(const char*) 按 ASCII 解释，引擎里所有含中文的字面量经 IPC 发出时都是乱码（实测 plugin.load 错误信息）；新增 Source/Text.h 的 u8() 并修正 40 处字面量。

- **2026-09-27（P4：FX 链与主总线，协议 v1 → v2）**：新增 `fx.setChain` / `fx.getChain` / `mixer.setMaster` / `mixer.get`（§4.13）；`getLevels` result 增加 `master` 字段（§4.7c）。引擎侧新增 `MixerGraph` 插入链（每轨 + 主总线各 8 槽）与延迟释放队列（`FxChain.h`）；主进程新增会话重放 `lastChains` / `lastMasterChain` / `lastMasterGain`（顺序：`loadSoundFont → setTrackPreset → setTrackVoice → fx.setChain → mixer.setMaster`）。插件实例装载仍属 P5/P6：`plugin` 非空时引擎返回可解释错误，槽参数（bypass/wet）可正常下发。握手版本号严格比对，不匹配即拒绝。

- **2026-09-05（v3.0.3）**：线格式不变。行为修订三处——① `bounce` 改消息线程异步执行（对齐 `scheduleSamples`）；② bounce 超时改按渲染时长动态估算（30s–20min）；③ 帧长上限 16 MiB 改为**收发两侧均校验**（此前仅接收侧校验）。另 PipeServer 断开时序改为「先锁内置空句柄再 CloseHandle」（消除对已关闭/复用句柄写入的竞态），不影响协议语义。
- **2026-09-05（波形声部与 ASIO 修复，随下一版本发布）**：新增 `setTrackVoice`（§4.6a，内置波形声部，与 loadSoundFont 互斥）；`openControlPanel` 改为立即应答 + 后台线程打开（§4.8）；applySetup 超时 15s→60s。主进程新增 `soundfont_loaded` 状态字段（/api/audio/status）。
- **2026-09-06（音频路线严格化）**：新增 `setTrackPreset`（§4.6b，SF2 预设选择）与 `click`（§4.6c，节拍器木鱼音）；/api/audio/status 新增 `default_soundfont`（音色目录首个 SF2 绝对路径，内置 piano/strings 的原生映射目标）；主进程会话重放顺序扩展为 `loadSoundFont → setTrackPreset → setTrackVoice`（`lastPresets` 每轨记录）；前端轨位约定：0–28 编曲、29 试听/编曲节拍器、30 钢琴窗 SF2/内置、31 钢琴窗波形/实时键盘。


### 4.14 VST3 插件宿主（P5）

引擎内嵌 JUCE 8 的 VST3 宿主（`JUCE_PLUGINHOST_VST3=1`，模块 `juce_audio_processors` +
`juce_gui_basics`）。扫描/实例化/参数/编辑器全部在**消息线程**执行；实例向音频线程
的发布用原子指针 + 延迟释放队列（沿用 P4 的 `FxTrashQueue`，P5 起它存的是可调用对象，
用于 shared_ptr 析构）。

**信号链**：`轨乐器（VST3 或 SF2/波形，互斥）→ 轨 FX 链 → 轨条 → 汇总 → 主 FX 链 → 压限`。
插件乐器的 MIDI 走独立 SPSC 环（`pluginMidi_`），与 SF2 同一投递纪律；MIDI 缓冲在
`audioDeviceAboutToStart` 预分配，回调内只 clear。

| 方法 | params | result / 错误 |
|---|---|---|
| `plugin.scan` | `{"dirs":["<目录>"],"cache":"<xml 路径>"}` | **立即**返回 `{"started":<bool>,"status":{...}}`。扫描异步执行（同步应答会把管道事务与心跳一起拖死，实测扫描期间后续请求全部超时）。`dirs` 为空则扫系统默认目录（Windows：`Common Files\VST3` 与 `%APPDATA%\VST3`） |
| `plugin.scanStatus` | `{"cache":"<xml 路径>"}` | `{"running","scanned","total","current","dirs","count"}`；`running=false` 时附带 `list`（清单 + 已注册格式），并把缓存写盘 |
| `plugin.scanCancel` | `{}` | 置取消标志（文件之间生效，已扫到的保留） |
| `plugin.list` | `{}` | `{"count","plugins":[{id,name,vendor,category,instrument,hasEditor,path}],"formats":["VST3"]}`。`formats` 为空=宿主没启用任何格式 |
| `plugin.loadCache` | `{"path"}` | `{"loaded":<bool>,"count":<int>}` |
| `plugin.load` | 乐器：`{"track":0,"id":"<id>"}`；效果：`{"track":0,"slot":0..7,"id"}` 或 `{"master":true,"slot":0..7,"id"}` | 宿主状态；未知 id / 实例化失败给可解释错误 |
| `plugin.unload` | 同 load（无 id） | 宿主状态 |
| `plugin.state` | `{}` | `{"instruments":[{track,id,name,hasEditor}],"effects":[{master,track,slot,name,hasEditor}],"trash":<int>}` |
| `plugin.params` | 目标同上 | `{"name","params":[{index,name,text,value,defaultValue,steps,automatable}]}` |
| `plugin.setParam` | 目标 + `{"index","value":0..1}` | 更新后的参数表 |
| `plugin.openEditor` | 目标 | `"editor opened"`；消息线程创建原生编辑器窗口（`PluginWindow`，无编辑器的插件给提示面板） |
| `plugin.closeEditor` | `{}` | `"editor closed"` |

**验收（`tools/engine_smoke -vst3dir <目录>`，本机实测）**：扫描真实插件（`8-Bit Crusher.vst3`、
`Krush.vst3`、`Serum.vst3` 三件套）→ 清单 3 个（乐器 1 / 效果 2）→ 效果器入 FX 槽 →
13 个参数 → setParam 回读 → 原生编辑器开/关 → 卸载 → 乐器装入轨道 → MIDI → 卸载，
全部通过。

**已知限制（如实记录）**：
1. 第三方**乐器**插件（Serum / Cymatics Voxity）在本机未授权，装入成功但渲染静音
   （实测轨道电平 0.0000）——乐器出声口径需要用户用已授权插件在桌面版实测，
   或后续用自建测试插件补一条无许可依赖的验收；
2. 插件扫描会把这些 DLL 载入引擎进程，个别插件的加载可能卡住；引擎用
   JUCE 的 dead-man's-pedal 列表记录并跳过，且扫描是异步 + 可取消的，
   但无法中断单个卡死的 DLL 调用（真宿主用独立扫描进程解决，列入后续）。

### 4.15 插件装载的健壮性口径（P5/P6 实测）

第三方插件"装不上"与"装了会崩"都是宿主必须接受的现实，实测（本机 4 个 VST3）：

| 插件 | 结果 | 说明 |
|---|---|---|
| Serum.vst3 | 装载成功（乐器） | 授权限制下渲染静音 |
| Cymatics Voxity.vst3 | 装载成功（效果，48 参数，编辑器可开） | 作为 smoke 的效果器用例 |
| 8-Bit Crusher.vst3 | **无法装载**（`Unable to load VST-3 plug-in file`） | 原装路径与复制路径、软链接路径结果一致——是该插件与本宿主的不兼容，不是路径问题 |
| Krush.vst3 | **加载时崩溃引擎进程** | 请求侧表现为 `读取帧失败: EOF`；supervisor 随后自动重启引擎，**后续请求（含再次装载其他插件）正常** |

由此确定三条口径：

1. **`plugin.load` 失败必须是可解释错误**，而不是静默成功（8-Bit Crusher 返回明确文案，前端据此提示）；
2. **插件崩溃不影响主程序**：引擎是独立进程，崩溃后由 supervisor 看门狗重启并重放会话（`lastVoices`/`lastChains`/`lastMasterGain`）——这就是 P6 验收 ④ 的机制；音频会短暂中断，主程序与工程数据不受影响；
3. **验收脚本不得因为"某个插件装不上"而整段变红**：`tools/engine_smoke -vst3dir <目录>` 逐个试装效果器，装不上的如实打印"跳过"并使用第一个可装载的；崩溃隔离由桌面版（带 supervisor）覆盖，独立 smoke 没有守护器，遇到崩溃会中止该段落并如实报告。

扫描侧另有一层保护：JUCE 的 dead-man's-pedal 列表记录"扫描中卡死/崩溃的插件"，下次扫描自动排到末尾或跳过；扫描本身是异步 + 可取消的（§4.14），不会拖死请求管线。