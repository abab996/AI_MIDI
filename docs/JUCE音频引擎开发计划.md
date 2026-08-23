# AI_MIDI × JUCE 原生音频引擎开发计划

> 版本：v1.0 ｜ 日期：2026-08-23
> 依据：《音频引擎选型与架构方案》（docs/音频引擎选型与架构方案.md），选型结论：**JUCE**
> 目标：为 AI_MIDI 引入低延迟原生音频引擎（ASIO / WASAPI），并为 VST3 插件宿主预留架构。

---

## 0. 一页总览

| 项 | 内容 |
|---|---|
| 最终形态 | `AI_MIDI.exe`（Go + Wails 主进程）+ `aimidi-engine.exe`（JUCE 原生引擎子进程），命名管道 IPC |
| 驱动覆盖 | ASIO、WASAPI（共享/独占）、DirectSound、Windows Audio（MME），JUCE AudioDeviceManager 统一抽象 |
| 合成能力 | SoundFont 2 渲染（tinySoundFont，MIT）+ 内置音色，替代前端 `synth.js` / `soundfont.js` 的发声职责 |
| 混音能力 | 每轨 gain/analyser 链 + 主总线压缩限幅，对齐前端 `audio_engine.js` 现有行为 |
| VST3 宿主 | 第三阶段落地，JUCE AudioPluginFormat + AudioProcessorGraph，GUI 经 HWND 嵌入 |
| 前端策略 | Web Audio 保留为降级/预览路径，前端时钟改为跟随引擎 timecode |
| 里程碑 | M0 基建 → M1 引擎骨架 → M2 合成迁移 → M3 编排多轨迁移 → M4 设备管理 UI → M5 VST3 宿主 → M6 打磨发布 |

---

## 1. 目标与非目标

**目标**
1. 支持 ASIO 专业声卡与 WASAPI 独占模式，缓冲区可低至 64 samples 无爆音；
2. 钢琴卷帘实时弹奏延迟（键入→出声）在 ASIO 下达到驱动+缓冲区的物理极限（<10ms 量级）；
3. 多轨编排播放由原生引擎渲染，解放前端 CPU，消除浏览器音频栈的不可控延迟；
4. 引擎进程崩溃不影响主程序，自动重启并恢复会话；
5. 架构预留 VST3 宿主、未来每插件一进程隔离的扩展位。

**非目标（本期不做）**
- VST2 兼容（Steinberg 已停止分发 VST2 SDK，无合法授权路径）；
- AU / AAX / CLAP 等其他插件格式；
- 音频录音与波形编辑（架构上预留输入通路，功能后置）；
- 移动端 / 非 Windows 平台（JUCE 本身跨平台，但本计划以 Windows 优先）。

---

## 2. 目标架构

```
┌────────────────────────────────────┐          ┌─────────────────────────────────────┐
│  AI_MIDI.exe (Go + Wails)          │          │  aimidi-engine.exe (JUCE 8, C++)    │
│                                    │          │                                     │
│  frontend (WebView2)               │          │  ┌─────────────────────────────┐   │
│   ├─ 钢琴卷帘 UI（跟随 timecode）   │          │  │ AudioDeviceManager          │   │
│   ├─ 编排窗口 UI                   │          │  │  ASIO/WASAPI/DS/MME         │   │
│   └─ 设置页·音频设备               │          │  ├─────────────────────────────┤   │
│                                    │  命名管道 │  │ SynthEngine                 │   │
│  internal/engine (Go)              │◄────────►│  │  tinySoundFont (SF2)        │   │
│   ├─ EngineClient (IPC 客户端)     │  双向     │  │  内置音色                    │   │
│   ├─ DeviceManager (设备元数据)     │          │  ├─────────────────────────────┤   │
│   ├─ TransportProxy (走带代理)     │          │  │ MixerGraph                  │   │
│   └─ ProcessSupervisor (生命周期)  │          │  │  轨 gain→主总线压缩/限幅     │   │
│                                    │          │  ├─────────────────────────────┤   │
│  internal/server                   │          │  │ PluginHost（M5 启用）        │   │
│   └─ /api/audio/* (设备/设置查询)   │          │  │  VST3 via AudioPluginFormat │   │
│                                    │          │  └─────────────────────────────┘   │
└────────────────────────────────────┘          └─────────────────────────────────────┘
```

**三条铁律**
1. **音频样本与实时回调永远不进 Go 进程**——Go GC 的 STW 会冻结线程，低缓冲下必爆音；
2. **Go 侧只发命令、收事件**：NoteOn/Off、走带、设备切换、参数变更；
3. **引擎是唯一样本时钟主**。前端播放头、Go 侧走带状态都跟随引擎 timecode，而非反向驱动。

**时钟同步方案**
- 引擎每 20ms 通过管道推送 timecode（采样位置 → 拍号/BPM 换算的 beat 位置）；
- Go 经 Wails 事件（`runtime.EventsEmit`）转发前端；
- 前端播放头渲染用 timecode 插值，音频本身由引擎出声，音画同步误差 <1 帧。

---

## 3. 目录结构规划（基于现有项目增量）

```
AI_MIDI-go/
├─ engine/                          ★新增：JUCE 引擎源码（独立构建单元）
│  ├─ CMakeLists.txt
│  ├─ Source/
│  │  ├─ Main.cpp                   子进程入口、IPC 主循环、崩溃守护
│  │  ├─ Ipc/
│  │  │  ├─ PipeServer.h/.cpp       命名管道服务端（\\.\pipe\AI_MIDI_ENGINE_<pid>）
│  │  │  ├─ Protocol.h              消息帧与消息类型定义（与 Go 侧共享语义）
│  │  │  └─ MessageDispatcher.h/.cpp
│  │  ├─ Audio/
│  │  │  ├─ EngineAudioDevice.h/.cpp    AudioDeviceManager 封装、设备枚举/持久化
│  │  │  ├─ SynthEngine.h/.cpp          复音合成器（tinySoundFont 集成）
│  │  │  ├─ MixerGraph.h/.cpp           轨混音 + 主总线压缩/限幅
│  │  │  └─ Transport.h/.cpp            走带、循环、节拍器、timecode 推送
│  │  ├─ Plugins/                        （M5 启用）
│  │  │  ├─ PluginHost.h/.cpp           VST3 扫描/加载/生命周期
│  │  │  └─ PluginWindow.h/.cpp         插件 GUI 的 HWND 承载
│  │  └─ ThirdParty/
│  │     └─ tinySoundFont/              tsf.h（MIT，单头 SF2 渲染）
│  └─ Tests/                            协议解析、混音数学的 C++ 单测（CTest）
│
├─ internal/
│  ├─ engine/                       ★新增：Go 侧引擎抽象层
│  │  ├─ types.go                   设备/音色/走带等共享类型
│  │  ├─ client.go                  EngineClient：管道连接、请求-响应、事件流
│  │  ├─ protocol.go                帧编解码（与 C++ Protocol.h 镜像）
│  │  ├─ supervisor.go              ProcessSupervisor：启动/心跳/崩溃重启/状态恢复
│  │  └─ fallback.go                引擎不可用时的降级策略（转 Web Audio 信号）
│  ├─ app/app.go                    ☆修改：Startup 拉起引擎，Shutdown 回收
│  ├─ config/
│  │  ├─ config.go                  ☆修改：新增 EngineExe 路径常量
│  │  └─ settings.go                ☆修改：settings.json 增加 audio 段
│  └─ server/
│     ├─ router.go                  ☆修改：注册 /api/audio/
│     └─ handler_audio.go           ★新增：设备列表/引擎状态/音频设置
│
├─ frontend/js/
│  ├─ engine/                       ★新增
│  │  ├─ engine_bridge.js           与 Go 侧的 Wails 绑定/HTTP 桥接、timecode 接收
│  │  └─ native_player.js           原生模式下的播放控制（替代直接驱动 AudioContext）
│  ├─ arrangement/audio_engine.js   ☆修改：双后端（native / webaudio 开关）
│  ├─ pianoroll/synth.js            ☆修改：同上
│  └─ pianoroll/soundfont.js        ☆保留：降级路径与 SF2 解析逻辑参考
│
├─ tools/                           ★新增
│  └─ build_engine.bat              CMake + MSVC 一键构建引擎
└─ RUN.bat / 启动.bat               ☆修改：校验 aimidi-engine.exe 存在
```

---

## 4. 引擎进程（aimidi-engine）设计

### 4.1 JUCE 模块裁剪
只启用需要的模块，控制体积与编译时间：

| 模块 | 用途 |
|---|---|
| `juce_core` / `juce_events` | 基础、消息循环 |
| `juce_audio_basics` | 缓冲、MIDI 消息 |
| `juce_audio_devices` | AudioDeviceManager、ASIO/WASAPI/DS/MME |
| `juce_audio_formats` | WAV/FLAC 素材解码（编排窗口音频素材） |
| `juce_dsp` | 主总线 Compressor/Limiter、EQ（对齐前端 DynamicsCompressor 行为） |
| `juce_audio_processors` | **仅 M5 引入**：VST3 宿主、AudioProcessorGraph |
| `juce_gui_basics` | 仅 M5 引入（插件 GUI 需要）；M1–M4 引擎为无窗口控制台进程 |

不启用：juce_audio_plugin_client（我们是宿主不是插件）、juce_opengl、juce_video 等。

### 4.2 设备管理（EngineAudioDevice）
- 枚举输出设备：类型（ASIO/WASAPI 独占/WASAPI 共享/DS/MME）、名称、支持采样率、通道数；
- 参数：采样率（44100/48000/88200/96000…）、缓冲区（64/128/256/512/1024/2048）；
- ASIO 控制面板透传：`AudioIODevice::showControlPanel()`（对应前端设置页"打开驱动控制面板"按钮）；
- 设备热插拔事件（`audioDeviceListChanged`）→ 推送 Go 侧 → 前端提示；
- 持久化：引擎不落盘，选择项由 Go 侧存 `settings.json`，启动时下发。

### 4.3 合成器（SynthEngine）
- **tinySoundFont（tsf）** 作 SF2 渲染核心：MIT 许可、单头文件、性能优秀、无 LGPL 传染风险（避开 FluidSynth）；
- 行为对齐现有前端实现（迁移基准，见 `frontend/js/pianoroll/soundfont.js`）：
  - 16 通道 × 复音（默认复音数 64，可配）；
  - NoteOn/Off、ProgramChange、PitchBend、CC（音量/表情/延音踏板 64）；
  - 音色库：项目内 `.sf2` 文件 + 4 个内置音色（piano/strings/epiano/bass，可预渲染为 SF2 或内置采样集）；
- 素材播放：编排窗口的音频素材轨用 `juce::AudioFormatReaderSource`，支持 Range 定位、采样率转换（SRC）到设备采样率。

### 4.4 混音图（MixerGraph）
对齐前端 `audio_engine.js` 的链路语义：

```
每轨: SynthEngine/素材源 → 轨 gain → （analyser 电平峰值采样）
      └→ 主总线 gain(0.9) → Compressor(threshold -6dB, ratio 12:1,
                              attack 3ms, release 120ms) → 输出
```
- 电平表数据：引擎每 50ms 计算各轨 RMS/峰值，随 timecode 一并推送，替代前端 AnalyserNode；
- 静音/独奏（mute/solo）在轨 gain 层实现。

### 4.5 走带（Transport）
- 播放/停止/定位（beat 与采样双单位）、循环段（对齐前端 `loop{on,start,end}`）、节拍器（可开关、重音）；
- BPM/拍号变更即时生效（对齐编排窗口 `bpm` 状态）；
- timecode 推送：`{ samplePos, beatPos, playing, loopWrapped }`，20ms 周期 + 状态突变即时推。

### 4.6 实时线程纪律（代码规范，写进引擎 README）
- 音频回调内禁止：内存分配、锁（仅用无锁环形队列）、文件/网络、日志输出；
- 命令队列：IPC 线程 → 无锁队列 → 音频线程消费；
- 所有参数变更用原子量或平滑过渡（gain 做 5ms 渐变，避免咔哒声）。

---

## 5. Go 侧设计（internal/engine）

### 5.1 接口定义（types.go，先行冻结）

```go
type DeviceManager interface {
    ListDevices() ([]AudioDevice, error)      // 类型/名称/采样率/通道
    ApplySettings(AudioSettings) error        // 切换设备/采样率/缓冲
    ShowControlPanel() error                  // ASIO 控制面板
}
type SynthEngine interface {
    SendEvents([]MidiEvent) error             // 批量 MIDI（低延迟路径）
    LoadSoundFont(path string) ([]Preset, error)
    SetProgram(trackID int, bank, program int) error
}
type Transport interface {
    Play/Stop/Locate/Loop/Metronome/SetTempo ...
    Timecodes() <-chan Timecode               // 引擎时间码流
}
type Engine interface { DeviceManager; SynthEngine; Transport; Status() EngineStatus }
```

### 5.2 IPC 协议（protocol.go ↔ Protocol.h）
- 管道：`\\.\pipe\AI_MIDI_ENGINE_<父进程pid>`，消息帧 = `[4B 长度][1B 类型][payload]`；
- 控制类消息用 JSON（可读、易调试）：设备切换、音色加载、走带控制、设置读写；
- 高频消息用紧凑二进制：MIDI 事件（3 字节原始消息 + 1 字节通道）、timecode（24B 定长）；
- 请求-响应带 `msg_id` 匹配；事件类消息单向推送（timecode/电平/设备变化/错误）；
- **协议文档单独成文**：`docs/引擎IPC协议.md`，Go/C++ 两侧实现共同遵循，版本号写入握手包。

### 5.3 进程监督（supervisor.go）
- 启动：从可执行文件同目录定位 `aimidi-engine.exe`（开发期可指向构建目录，走 `settings.json` 覆盖）；
- 握手：3 秒超时，失败则进入降级模式并通知前端；
- 心跳：1 秒周期，连续 3 次丢失判定崩溃；
- 重启恢复：自动重启 + 重放最近会话快照（当前设备设置、已加载音色、走带状态——快照由 Go 侧维护）；
- 退出：主程序 `Shutdown` 时发送优雅退出命令，等待 500ms 后强杀。

### 5.4 HTTP API 增量（handler_audio.go）
| 路由 | 方法 | 说明 |
|---|---|---|
| `/api/audio/devices` | GET | 设备列表（透传引擎枚举结果） |
| `/api/audio/settings` | GET/POST | 读写音频设置（落 settings.json + 下发引擎） |
| `/api/audio/engine/status` | GET | 引擎进程状态、当前延迟、采样率 |
| `/api/audio/control` | POST | ASIO 控制面板等控制操作 |

实时演奏路径不走 HTTP：前端经 Wails 绑定方法（`appInstance` 新增 `NoteOn/NoteOff` 等）直达 `internal/engine`，避免 HTTP 开销。

### 5.5 配置扩展（settings.json）
```json
"audio": {
  "engine_enabled": true,
  "driver": "ASIO",            // ASIO | WASAPI_EXCLUSIVE | WASAPI_SHARED | DirectSound | MME
  "device": "Focusrite USB ASIO",
  "sample_rate": 48000,
  "buffer_size": 128,
  "soundfonts": ["Library/soundfonts/xxx.sf2"]
}
```

---

## 6. 前端改造

| 模块 | 改造 |
|---|---|
| `engine_bridge.js`（新增） | 统一入口：检测引擎可用性（`/api/audio/engine/status`）、接收 timecode（Wails 事件）、暴露演奏/走带方法 |
| `audio_engine.js` | 双后端：`native` 模式把发声调度交给引擎，本地仅保留 UI（电平表改读引擎推送数据）；`webaudio` 模式保持现状作降级 |
| `synth.js` / `soundfont.js` | 同上开关化处理；SF2 解析代码保留（降级路径 + 可用于前端音色列表预读） |
| 播放头渲染 | 由 AudioContext 时钟锚点（`_anchorCtxTime`）改为引擎 timecode 插值 |
| 设置页 `settings.html` | 新增"音频设备"区块：驱动/设备/采样率/缓冲区下拉、延迟估算显示、"打开驱动控制面板"按钮、引擎状态指示灯 |
| 降级提示 | 引擎不可用时横幅提示"原生引擎未运行，当前为浏览器音频（延迟较高）" |

---

## 7. 构建与打包

1. **工具链**：Visual Studio 2022（MSVC v143）+ CMake ≥ 3.25 + JUCE 8（CMake 集成，`juce_add_console_app` 起步，M5 起改 `juce_add_gui_app`）；
2. **依赖获取**：JUCE 以 git submodule 引入 `engine/ThirdParty/JUCE`（锁定版本标签）；ASIO SDK 头文件按 Steinberg 许可下载后放入 `engine/ThirdParty/asio_sdk`（许可文本随仓库记录，不入 git 则写入构建文档）；
3. **构建脚本**：`tools/build_engine.bat` → Release x64 → 输出 `aimidi-engine.exe` 复制到项目根（与 `AI_MIDI.exe` 同级）；
4. **wails build 集成**：在 `wails.json` 增加构建钩子，`wails build` 前先跑引擎构建；
5. **启动校验**：`RUN.bat` / `启动.bat` 增加引擎文件存在性检查；
6. **发布清单**：`AI_MIDI.exe` + `aimidi-engine.exe` + `Library/` + 配置，引擎缺省时主程序仍可运行（降级模式）。

---

## 8. VST3 宿主专项（M5 展开）

| 事项 | 方案 |
|---|---|
| 扫描 | 标准目录（`C:\Program Files\Common Files\VST3` 等）+ 用户自定义目录（设置项）；JUCE KnownPluginList 持久化 |
| 加载 | `AudioPluginFormatManager`（VST3）→ `AudioPluginInstance`，插入 MixerGraph 的轨插入槽或主总线 |
| 参数 | 参数元数据经 IPC 同步给前端，前端渲染参数面板（比插件原生 GUI 更贴合产品风格）；原生 GUI 作为备选入口 |
| GUI 嵌入 | 插件编辑器以 HWND 挂入 Wails 窗口的原生子区域（M5 先做独立弹窗，嵌入主窗口排 M6） |
| 崩溃隔离 | 第一阶段：插件崩溃 = 引擎进程崩溃 → supervisor 恢复（可接受）；第二阶段：每插件独立子进程（视稳定性数据决定是否做） |
| 许可 | Steinberg vst3sdk 采用其免费专有许可（签署即可，注意分发条款）；JUCE 商用许可覆盖宿主能力 |
| MIDI 路由 | 乐器轨：走带事件 + 实时弹奏 → 插件；效果器：轨输出 → 插件链 → 回混音图 |

---

## 9. 许可与仓库策略（已定案）

**背景结论**：JUCE 8 已取消 AGPL 开源选项，全部许可层级均为商业 EULA；
EULA 第 2.3 条禁止使 JUCE 受制于任何开源许可证（含要求披露源码、允许衍生、
允许免费再分发三项，均与 Apache-2.0 冲突）。因此 **JUCE 源码不能进入本项目的
Apache-2.0 开源仓库**，与项目是否收费无关。

### 9.1 最终策略：混合许可 + 仓库拆分

| 组件 | 许可 | 仓库 |
|---|---|---|
| Go 主程序 + 前端 + 文档 | Apache-2.0 | 本开源仓库 |
| `engine/`（JUCE 引擎源码） | 闭源（JUCE EULA Starter 免费档） | 独立私有仓库 |
| `aimidi-engine.exe`（编译产物） | 随 Release 附件分发（EULA 1.9/1.11 允许） | 不入任何仓库 |

### 9.2 合规要点

- [ ] **JUCE Starter 免费档**：零收入即合规；注意收入上限含捐赠/赞助
      （个人按全部相关收入计），12 个月累计超 $20,000 须升级 Indie 档或停用引擎；
- [ ] **主仓库防护**：`.gitignore` 已排除 `engine/`、`third_party/`、`ThirdParty/`、
      `asio_sdk/` 及 `*.exe`；引擎私有仓库建议反向忽略，避免误推主仓库内容；
- [ ] **席位规则（EULA 1.7）**：修改引擎代码的人需要 JUCE 席位；仅编译/测试的
      机器不需要。引擎部分暂不接受外部 PR；
- [ ] **ASIO SDK**：接受 Steinberg 许可条款；"ASIO" 名称/标识使用遵守其品牌规范；
- [ ] **VST3 SDK（M5）**：签署 Steinberg 专有许可；产品内按条款展示 "VST3 compatible" 声明；
- [ ] **tinySoundFont**：MIT，保留版权声明；
- [ ] **明确不碰**：VST2 SDK（已停止分发）、任何来源不明的 VST2 头文件；
- [ ] **发布页声明**：说明引擎为闭源组件及 JUCE 版权归属（Raw Material Software）。

---

## 10. 里程碑与验收标准

### M0 基建（不依赖 JUCE，可立即开工）
- `internal/engine` 接口与共享类型冻结；
- 《引擎IPC协议》文档定稿（消息字典、帧格式、握手、版本号）；
- `settings.json` audio 段 + `/api/audio/settings` 读写；
- **验收**：协议文档评审通过；Go 侧用 mock 引擎（纯 Go 回显进程）跑通全部消息类型单测。

### M1 引擎骨架
- CMake 工程、JUCE submodule、构建脚本、`wails build` 钩子；
- 管道服务端 + 握手 + 心跳；supervisor 启动/崩溃重启；
- AudioDeviceManager 接入：默认设备出声（440Hz 测试音）；
- **验收**：主程序启动自动拉起引擎；杀引擎进程 3 秒内自动恢复；WASAPI 下播放测试音无爆音。

### M2 钢琴卷帘迁移
- SynthEngine（tinySoundFont）+ MIDI 事件通路；
- 前端演奏路径切原生（带开关）；timecode 跟随；
- **验收**：ASIO 128 samples 下连续弹奏无爆音、无丢音；切换回 `webaudio` 模式功能不变。

### M3 编排窗口多轨迁移
- MixerGraph（轨 gain + 主总线压缩/限幅）+ 素材轨解码播放 + 走带/循环/节拍器；
- 电平表数据改由引擎推送；
- **验收**：现有编排工程在原生引擎下播放效果与 Web Audio 版主观一致（AB 对比）；16 轨负载下缓冲 128 无 xrun。

### M4 设备管理与设置
- 设备枚举/切换热生效、ASIO 控制面板、热插拔提示、延迟估算显示；
- 设置页"音频设备"区块完整上线；
- **验收**：至少 3 类设备实测（专业声卡 ASIO、板载 WASAPI 共享、蓝牙设备降级表现），切换设备播放不中断超过 1 秒。

### M5 VST3 宿主
- 插件扫描/加载/参数面板（前端渲染）/乐器轨与效果插入；
- **验收**：3 款主流免费 VST3（乐器×2、效果×1）加载、出声、参数控制、工程保存恢复全部通过；插件崩溃后主程序存活并可恢复。

### M6 打磨与发布
- 插件 GUI 嵌入主窗口、性能剖析（CPU/内存/延迟基准报告）、错误上报、发布清单固化；
- **验收**：完整回归 M1–M5 验收项；发布包含引擎的完整安装包。

依赖关系：M0 → M1 → M2 → M3；M4 可与 M2 并行；M5 依赖 M3；M6 收尾。

---

## 11. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| Go/C++ 双协议实现漂移 | 联调成本高 | 协议文档先行（M0）；消息类型带版本；两侧共享一份消息字典测试用例 |
| 团队 C++/JUCE 经验不足 | M1 推进慢 | M1 范围压到最小（出声+管道）；JUCE 官方文档与示例工程对照；必要时先做 JUCE 原型验证周 |
| ASIO 驱动兼容性（各家声卡怪癖） | 特定设备爆音/崩溃 | 依赖 JUCE 已踩过的坑；M4 建实测设备矩阵；引擎崩溃隔离保底 |
| 引擎进程间时延抖动 | 弹奏手感 | 演奏路径走管道直连（无 HTTP）；MIDI 消息批量合并发送 |
| JUCE 许可成本/条款变化 | 商务风险 | 采购前留存官网条款快照；许可文件入档 `docs/licenses/` |
| VST 插件质量参差导致崩溃 | 用户体验 | supervisor 会话快照恢复；崩溃日志带回放上下文；M5 后评估每插件一进程 |

---

## 12. 立即可启动的事项（M0 清单）

1. 冻结 `internal/engine` 接口草案（本计划 5.1 为初稿）；
2. 撰写《引擎IPC协议》消息字典初稿；
3. `settings.json` 增加 audio 段 + `handler_audio.go` 骨架；
4. 采购/确认 JUCE 商用许可，创建 `docs/licenses/` 归档；
5. 安装 Visual Studio 2022（C++ 工作负载）与 CMake，验证 `juce_add_console_app` 最小工程可编译。

> 备注：本计划不含工期估算；里程碑按依赖顺序推进，每阶段验收通过后再进入下一阶段。
