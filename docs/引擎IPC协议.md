# AI_MIDI 引擎 IPC 协议（v1）

> 版本：1 ｜ 日期：2026-08-23 ｜ 修订：2026-09-05（见文末修订记录，线格式不变）
> 适用双方：`AI_MIDI.exe`（Go 主进程，客户端）↔ `aimidi-engine.exe`（JUCE 引擎，服务端）
> 实现镜像：Go 侧 `internal/engine/protocol.go`（M1 后续落地）；C++ 侧 `engine/Source/Ipc/Protocol.h`
> **本文为协议唯一权威来源**，两侧实现与 engine/README 摘要如有出入，以本文为准。

---

## 1. 管道命名与连接规则

- 管道名：`\\.\pipe\AI_MIDI_ENGINE_<引擎进程PID>`
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
| result | `{"name":"aimidi-engine","version":"0.1.0","protocolVersion":1,"capabilities":["devices","testTone"]}` |
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
| result | `{"levels":[<float>, ...]}`（32 轨峰值电平） |

### 4.7d `setLoop`（走带循环）
| | |
|---|---|
| params | `{"on":true,"start":<拍>,"end":<拍>}` |
| result | `{}` |

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

- **2026-09-05（v3.0.3）**：线格式不变。行为修订三处——① `bounce` 改消息线程异步执行（对齐 `scheduleSamples`）；② bounce 超时改按渲染时长动态估算（30s–20min）；③ 帧长上限 16 MiB 改为**收发两侧均校验**（此前仅接收侧校验）。另 PipeServer 断开时序改为「先锁内置空句柄再 CloseHandle」（消除对已关闭/复用句柄写入的竞态），不影响协议语义。
- **2026-09-05（波形声部与 ASIO 修复，随下一版本发布）**：新增 `setTrackVoice`（§4.6a，内置波形声部，与 loadSoundFont 互斥）；`openControlPanel` 改为立即应答 + 后台线程打开（§4.8）；applySetup 超时 15s→60s。主进程新增 `soundfont_loaded` 状态字段（/api/audio/status）。
- **2026-09-06（音频路线严格化）**：新增 `setTrackPreset`（§4.6b，SF2 预设选择）与 `click`（§4.6c，节拍器木鱼音）；/api/audio/status 新增 `default_soundfont`（音色目录首个 SF2 绝对路径，内置 piano/strings 的原生映射目标）；主进程会话重放顺序扩展为 `loadSoundFont → setTrackPreset → setTrackVoice`（`lastPresets` 每轨记录）；前端轨位约定：0–28 编曲、29 试听/编曲节拍器、30 钢琴窗 SF2/内置、31 钢琴窗波形/实时键盘。
