# AI_MIDI × DAW 宿主工程导出开发计划

> 版本：v1.0 ｜ 日期：2026-08-27  
> 目标：为 AI_MIDI 引入一键导出主流数字音频工作站（FL Studio、Studio One 等）工程格式的能力，打通“AI 编曲创作 → 专业 DAW 深度混音/母带/实录”的完整生产力链路。

---

## 0. 一页总览

| 项 | 内容 |
|---|---|
| **核心目标** | 将 AI_MIDI 项目编排数据（`arrangement.json`、音轨、MIDI/音频剪辑、混音参数）无损转换为专业宿主原生工程 |
| **首期覆盖宿主** | **Studio One 6.5+**（基于开放标准 `DAWproject`）、**FL Studio**（基于原生 `.flp` 二进制流）、**全宿主通用**（Type 1 多轨 SMF MIDI） |
| **后续扩展目标** | Ableton Live (`.als` / gzip-xml)、Cubase/Nuendo (`MusicXML` / `AAF`)、Logic Pro (`DAWproject` 桥接) |
| **技术架构** | 建立 `DAWExportIR`（通用宿主抽象中间表示层），实现解耦的“工程提取 → IR 转换 → 目标格式编码器 → 资产打包”流水线 |
| **后端语言** | Go 原生实现（零外部 C/C++ 依赖，高并发内存安全打包） |
| **交付形态** | 1. 独立工程文件（如 `.dawproject`、`.flp`、`.mid`）<br>2. 包含分轨音频与采样的工程资产包（ZIP） |
| **里程碑路径** | M0 抽象中间层与 Type 1 MIDI → M1 DAWproject（Studio One）→ M2 FLP 原生生成（FL Studio）→ M3 前端交互与真机验证 → M4 进阶参数映射 |

---

## 1. 背景与业务价值

### 1.1 痛点分析
当前 AI_MIDI 支持导出单文件/多文件 MIDI 以及混音渲染后的全曲 WAV 格式。但在专业音乐人与制作人的实际工作流中：
- 仅导出单轨 MIDI 需要用户在宿主中手动新建多条音轨并重新导入、命名、着色与排布；
- 导出的渲染音频无法在宿主中继续调整各个音符与编排切片；
- 用户在 AI_MIDI 完成灵感创作与结构搭建后，缺乏直接“移步”到 FL Studio 或 Studio One 进行挂载第三方 VST3、实录人声、精细混音与母带的无缝接口。

### 1.2 目标与非目标

**核心目标：**
1. **Studio One 完美兼容**：导出 `.dawproject` 格式，Studio One 6.5+ 原生一键双击无缝打开，完整保留轨道层级、颜色、音量/声相、MIDI 剪辑与音频剪辑时间轴；
2. **FL Studio 原生兼容**：导出 `.flp` 二进制工程，FL Studio 原生打开，Channel Rack、Playlist（播放列表）、混音台（Mixer Track）及音符属性精准映射；
3. **通用兜底方案**：导出 Type 1 多轨标准 MIDI 文件，拖入任意宿主均可自动按音轨拆分并对齐时间轴；
4. **轻量与自闭环**：Go 语言原生序列化与打包，无需安装任何宿主环境或第三方大型依赖。

**非目标（本期不做）：**
- 逆向加载宿主工程文件逆向导入 AI_MIDI（本期聚焦**单向导出**）；
- 第三方专有合成器预设二进制映射（如 Serum/Massive 专有音色 patch，优先映射标准 GM/SoundFont 对应轨道命名与通用参数）；
- 宿主私有加密工程格式（如 Cubase `.cpr` 官方私有加密协议，推荐采用 MusicXML/AAF/MIDI 分轨替代）。

---

## 2. 格式技术原理与协议选型

```
                   ┌───────────────────────────────┐
                   │   AI_MIDI arrangement.json    │
                   │   (Tracks, Clips, BPM, etc.)  │
                   └──────────────┬────────────────┘
                                  │
                                  ▼
                   ┌───────────────────────────────┐
                   │    DAWExportIR (统一中间层)    │
                   └──────┬───────┼───────┬────────┘
                          │       │       │
            ┌─────────────┘       │       └─────────────┐
            ▼                     ▼                     ▼
┌───────────────────────┐ ┌───────────────┐ ┌───────────────────────┐
│ DAWproject Generator  │ │ FLP Generator │ │ SMF Type 1 Generator │
│ (PreSonus Studio One) │ │  (FL Studio)  │ │   (Universal DAWs)    │
└───────────┬───────────┘ └───────┬───────┘ └───────────┬───────────┘
            │                     │                     │
            ▼                     ▼                     ▼
     .dawproject (ZIP)        .flp (Binary)       .mid (Multitrack)
```

### 2.1 Studio One 方案：`DAWproject`（开放工程标准）

- **标准制定方**：PreSonus（Studio One）与 Bitwig 联合发布。
- **宿主支持**：Studio One 6.5 及以上原生内置支持，双击直接加载。
- **文件本质**：遵循规范的 ZIP 容器，核心结构如下：
  ```
  my_song.dawproject
  ├── mimetype                  # 声明格式 "application/x-dawproject"
  ├── project.xml               # 核心工程描述文件（结构、音轨、剪辑、音符、混音）
  ├── metadata.xml              # 工程元数据（作者、标题、BPM、创建时间）
  └── audio/                    # 引用的分轨音频资源（WAV/FLAC 等）
      ├── clip_vocal_01.wav
      └── drum_loop_02.wav
  ```
- **映射能力**：
  - 时间结构：BPM、拍号（Time Signature）、全局拍数时间轴；
  - 轨道体系：Track Name、Color (Hex/RGBA)、Mute、Solo、Volume (dB)、Pan (-1.0 ~ 1.0)；
  - 剪辑系统：Audio Clip（时间范围、偏移 offset、淡入淡出 fade）、MIDI Clip（包含完整 Note On/Off/Pitch/Velocity）。

### 2.2 FL Studio 方案：原生 `.flp` 二进制事件流

- **格式特征**：FL Studio 专有的 Chunk 二进制流架构，由文件头和连续的事件块组成：
  - `FLhd`（Header Chunk）：声明通道数、时间基准 PPQ（默认 96/480 ticks/beat）；
  - `FLdt`（Data Chunk）：由事件字节（Event ID + 变长数据）组成的连续流。
- **关键事件映射表**：
  | 事件名 | 含义 | 映射来源 |
  |---|---|---|
  | `EVENT_TEMPO` / `EVENT_FINE_TEMPO` | 工程速度 BPM | `arrangement.bpm` |
  | `EVENT_TRACK_TITLE` | 轨道/通道名称 | `track.name` |
  | `EVENT_TRACK_COLOR` | 轨道颜色 | `track.color` |
  | `EVENT_TRACK_VOL` / `EVENT_TRACK_PAN` | 混音台音量与声相 | `track.volume`, `track.pan` |
  | `EVENT_PATTERN_NOTES` | 音符序列 | `clip.notes` (Pitch, Start, Length, Velocity) |
  | `EVENT_PLAYLIST_ITEMS` | 播放列表剪辑排布 | `clip.start`, `clip.length`, `clip.offset` |
- **交付形式**：
  - 纯 MIDI 编排：直接生成单个 `.flp`；
  - 混合音频编排：生成 `.flp` + `audio/` 相对路径引用的 ZIP 打包工程。

### 2.3 全宿主通用方案：Type 1 多轨标准 MIDI (SMF1)

- **当前现状**：现有 `internal/midi/smf_writer.go` 生成的是 Type 0 单轨文件。
- **升级目标**：
  - 生成 Standard MIDI File Type 1 规范文件；
  - Track 0 为全局 Master Track（包含 Tempo Meta Event、Time Signature、Key Signature、Marker/小节标记）；
  - Track 1..N 为独立乐器音轨，各自包含独立的 `MTrk` 块、`0x03` 音轨名称、`0x20` MIDI 通道分配以及绝对时间轴对齐的音符序列。

---

## 3. 系统架构与模块设计

### 3.1 统一中间表示层（`DAWExportIR`）
定义在 `internal/export/model.go`，充当适配器模式的核心桥梁，屏蔽各 DAW 底层协议差异：

```go
type ProjectIR struct {
    Title       string
    BPM         float64
    TimeSigNum  int
    TimeSigDen  int
    SampleRate  int
    Tracks      []TrackIR
    MasterBus   BusIR
}

type TrackIR struct {
    ID          string
    Name        string
    ColorHex    string
    Volume      float64 // 0.0 ~ 1.0 (内部换算为 dB)
    Pan         float64 // -1.0 ~ 1.0
    Mute        bool
    Solo        bool
    Type        TrackType // TrackTypeMIDI / TrackTypeAudio / TrackTypeHybrid
    Clips       []ClipIR
}

type ClipIR struct {
    ID          string
    Name        string
    Type        ClipType // ClipTypeMIDI / ClipTypeAudio
    StartBeat   float64
    LengthBeat  float64
    OffsetBeat  float64
    Gain        float64
    Mute        bool
    // Audio 专有
    AudioFile   string
    FadeInBeat  float64
    FadeOutBeat float64
    // MIDI 专有
    Notes       []NoteIR
}

type NoteIR struct {
    Pitch       uint8   // 0 - 127
    StartBeat   float64 // 相对 Clip 起始点
    LengthBeat  float64
    Velocity    uint8   // 0 - 127
}
```

### 3.2 目录与代码划分

```
internal/export/
├── model.go              # DAWExportIR 数据结构定义
├── converter.go          # arrangement.json -> ProjectIR 转换与时间轴对齐
├── service.go            # ExportService：对外统一导出调度器
├── dawproject/           # Studio One / Bitwig 导出引擎
│   ├── xml_schema.go     # DAWproject XML Structs (序列化 tags)
│   ├── generator.go      # ProjectIR -> project.xml 生成
│   └── packager.go       # ZIP 容器封装与 audio 资产打包
├── flp/                  # FL Studio 导出引擎
│   ├── constants.go      # FLP Chunk & Event IDs
│   ├── binary_writer.go  # 二进制大端/小端/变长字节写入器
│   └── generator.go      # ProjectIR -> .flp 二进制流生成
└── smf/                  # 标准 MIDI 导出引擎
    └── smf1_writer.go    # Type 1 多轨 MIDI 序列化器
```

---

## 4. 接口与交互设计

### 4.1 REST API 端点

| 方法 | 路径 | 参数 | 响应 | 描述 |
|---|---|---|---|---|
| `GET` | `/api/projects/{id}/export/daw` | `format=dawproject` | `application/octet-stream` (`.dawproject`) | 导出 Studio One 工程包 |
| `GET` | `/api/projects/{id}/export/daw` | `format=flp` | `application/octet-stream` (`.flp` 或 `.zip`) | 导出 FL Studio 工程文件 |
| `GET` | `/api/projects/{id}/export/daw` | `format=midi_type1` | `audio/midi` (`.mid`) | 导出 Type 1 多轨通用 MIDI |
| `GET` | `/api/projects/{id}/export/daw` | `format=stems` | `application/zip` | 导出分轨音频 + 多轨 MIDI 整合包 |

### 4.2 前端交互设计
1. **主菜单扩展**：在顶部控制栏的「导出」按钮下拉中，新增「导出宿主工程」专区：
   - 🟧 **FL Studio 工程 (`.flp`)**：附带提示“支持 FL Studio 20/21/24 直接打开”；
   - 🟦 **Studio One 工程 (`.dawproject`)**：附带提示“支持 Studio One 6.5+ / Bitwig 打开”；
   - 🎹 **多轨 MIDI 文件 (`Type 1 .mid`)**：附带提示“兼容 Cubase/Logic/Ableton/ProTools 等全宿主”；
2. **导出选项弹窗**：支持勾选“是否包含音频剪辑素材”、“是否包含空白小节留白”等个性化选项。

---

## 5. 研发实施阶段与里程碑

### 阶段一（M0）：通用中间层与多轨 MIDI 引擎（预计 1-2 天）
- [ ] 编写 `internal/export/model.go` 与 `converter.go`，将 `arrangement.json` 转换为规范化的 `ProjectIR`；
- [ ] 实现 `internal/export/smf/smf1_writer.go`，支持多轨 MTrk 分轨与全局 Meta 轨道；
- [ ] 编写单元测试：验证各轨道独立时间轴、音符绝对位置与速度标记；
- [ ] 交付验证：导出的 `.mid` 文件拖入 FL Studio / Studio One，能正确自动识别为多音轨。

### 阶段二（M1）：Studio One 原生兼容 —— DAWproject 实现（预计 2-3 天）
- [ ] 按照 DAWproject 官方 XML Schema 编写 Go 结构体映射；
- [ ] 实现 `dawproject/generator.go`，将 `ProjectIR` 映射为 `project.xml` 与 `metadata.xml`；
- [ ] 实现 `dawproject/packager.go`，完成 zip 格式打包并内嵌 `audio/` 目录；
- [ ] 交付验证：在 Studio One 6.5+ 中实测打开，验证音轨颜色、音量、声相、MIDI 剪辑与音频剪辑对齐精度。

### 阶段三（M2）：FL Studio 原生兼容 —— FLP 二进制引擎（预计 3-4 天）
- [ ] 构建 FLP 二进制 Chunk 写入器（处理 `FLhd` 与 `FLdt` 变长整数编码）；
- [ ] 实现 FLP 核心事件映射（Tempo、Channel、Track Color/Name、Pattern Note 列表、Playlist 排布）；
- [ ] 实现包含音频切片的 FLP ZIP 打包模式；
- [ ] 交付验证：在 FL Studio 20/21 中直接双击打开 `.flp`，验证播放列表与通道机架（Channel Rack）是否正确生成。

### 阶段四（M3）：前端交互与综合交付（预计 1-2 天）
- [ ] 在 `router.go` 与 `handler_project.go` 注册 `/api/projects/{id}/export/daw` 路由；
- [ ] 在前端主导航栏与编曲窗口添加导出入口 UI；
- [ ] 完善错误边界处理（如工程无任何剪辑时的安全导出、缺失音频资产时的占位处理）；
- [ ] 编写端到端自动化测试与回归测试用例。

---

## 6. 风险评估与应对措施

| 风险点 | 影响程度 | 应对措施 |
|---|---|---|
| **FL Studio 版本差异导致的兼容性异常** | 中 | FLP 二进制结构采用最稳健的向下兼容事件集（FL 12/20/21/24 通用规范），避免使用最新实验性私有 Tag。 |
| **音频剪辑路径在不同操作系统间的反斜杠问题** | 高 | DAWproject 与 FLP 内一律采用标准的相对 POSIX 路径（`audio/sample.wav`），并在打包为 ZIP 时自动重命名规范化。 |
| **大型工程打包导致内存峰值过高** | 中 | 采用流式 `zip.Writer` 与 `io.Copy` 管道处理，避免将数十兆分轨音频一次性全部读入内存。 |
| **用户在不同采样率宿主中打开的音画同步** | 低 | 在 ProjectIR 与工程元数据中显式写入工程当前定义的采样率（44.1kHz / 48kHz）与 BPM，由宿主自动对齐网格。 |
