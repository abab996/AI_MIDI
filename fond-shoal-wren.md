# MelodyMate 架构升级实施计划

> 范围：品牌改名 MelodyMate → 音源注册表（声音管理）→ 输出机架乐器箱化 → 素材库三页签 → 混音台 → VST3 宿主 → MM（.mm）插件平台
> 涉及两个仓库：**主仓库**（Go + TS + Wails，`D:\pyx\AI_MIDI-go`）与**引擎仓库**（C++/JUCE，`engine/`，独立 git）
> 执行方式（用户已确认）：**全量规划、分阶段验收**；每阶段独立可验证、可暂停

---

## 一、已确认决策

| # | 决策项 | 结论 |
|---|---|---|
| D1 | 改名范围 | **彻底改名（含内部标识）**：exe 名、管道名、模块名、存储键、更新地址、仓库 URL 全部换新，并配套数据迁移 |
| D2 | MM 插件 DSP 内核 | **原生 C++ 高性能路线**（VST 级）；AI 负责功能/UI 基础代码，音频 I/O 由 SDK 托管、默认不给 AI；敏感能力需用户授权；提供一键编译工具产出 .mm |
| D3 | VST 界面 | **原生插件编辑器窗口**（引擎进程内开窗，引入 juce_gui_basics），同时保留通用参数面板供 AI 与自动化使用 |
| D4 | 计划范围 | 全量规划、分阶段验收（P-1 分支 + P0–P7 七个里程碑） |
| D5 | 分支策略 | 改动量大，动工前先开新分支：主仓库 `feat/melodymate`、引擎仓库 `feat/melodymate-engine`（详见 P-1） |

---

## 二、现状基线（调研结论，均已核实）

**音源/SF2**
- 三个入口分散：钢琴窗弹窗（`chat.html:717` `#soundLibModalOverlay`，绑定 `chat.ts:4315-4352`）、机架导入（`chat.html:697` `#arrRackAddSf2Btn` → `arrange.ts:3681-3730`）、设置页仅有状态文本（`settings.html:278`）
- 存储双份：磁盘 `Library/soundfonts/*.sf2`（`handler_soundfont.go:23` POST，tmp+rename）+ IndexedDB `AI_MIDI_SoundLibrary`（`sound_library.ts:78-129`，含 diskPath）。**磁盘为真源，无清单/索引**；默认音色=目录按名排序首个（`supervisor.go:1062`）
- 引擎每轨独立 tsf 解析（`SynthEngine.cpp:352`），无路径去重 → 32 轨同库≈32 份内存；`tsf_copy` 共享能力未用
- 内置 piano/strings = GeneralUser GS 的 (0,0)/(0,48)（`pianoroll.ts:746`、`arrange.ts:4016`），无独立定义

**输出机架**（`chat.html:674-714`，逻辑 `arrange.ts:3602-4100`）
- 条目来源是 4 处硬编码拼接：硬编码 synth、硬编码 builtin 钢琴/弦乐、SF2 库首预设、扫 clip 得采样——无统一音源仓库
- 轨道音源仅 3 型 `synth{wave}/builtin{tone}/sf2{libId,presetId}`（`arrange.ts:317-328/2674-2691`），存 arrangement.json
- 钢琴窗音色与编曲轨、机架**完全不相通**（`pianoroll.ts:3452-3660`，`soundSource` 仅内存变量）

**素材库**（`chat.html:353-361`）
- 数据源仅 `settings.material_dirs`（`handler_arrangement.go:39-95,141-225`），文件夹树 + 拖拽成音频 clip；无项目内素材、无上传、无分组

**引擎**
- JUCE **8.0.15**（自带 VST3 SDK）；`CMakeLists.txt:71` 注释明确「M5 引入 VST 时追加 juce_audio_processors / juce_gui_basics」；`Source/Audio/PluginHost.h` 已有扫描/实例化骨架但**未进 target_sources**；`Source/Plugins/` 空目录已预留
- 32 轨（`MixerGraph.h:17`）；每轨一个 SynthEngine；实时与离线 bounce 共用 render + `mixer_.process`（`EngineAudioDevice.cpp:827`）
- 监管：`supervisor.go` 崩溃重启熔断、状态重放（applySetup → loadSoundFont → setTrackPreset → setTrackVoice，顺序不可换）
- 轨位约定（`engine_bridge.ts:14-16`）：0-28 编曲、29 试听/节拍器、30 钢琴窗 SF2、31 钢琴窗波形/键盘

**AI 控制通路**
- MCP 工具 `internal/mcp/registry.go`（8 个）+ `executor.go`，注入 `chat/pipeline.go:281-283`
- SSE 已有通用事件通道（`files`/`download` 等，`pipeline.go:549-582` ← `chat.ts:2190-2212`）可作为前端刷新信号
- Go supervisor 已暴露 `SetTrackMix/SetTrackVoice/SetTrackPreset/LoadSoundFontTrack/GetLevels` 等引擎直调 API（`supervisor.go:400-495/998-1063/1274`）
- 编曲数据仅由前端整份 PUT（`handler_project.go:358-380`），**AI 没有服务端操作编曲状态的入口**

**命名与数据落地**
- 用户可见名称集中在：`internal/config/config.go:34` WindowTitle、`wails.json`、三个 HTML `<title>`+关于页+免责声明+Star 弹窗、`AI_MIDI.iss`、README×2
- 内部标识：管道 `\\.\pipe\AI_MIDI_ENGINE_<pid>`（`client.go:58` ↔ `Main.cpp:539`）、单实例互斥体、Go module `aimidi`（~100 文件 import）、C++ 命名空间 `aimidi::audio/ipc`、IndexedDB `AI_MIDI_SoundLibrary`、localStorage `ai-midi-*`/`aimidi_star_seen`、日志 `output/ai_midi.log`、更新域 `aimidi-r2.baimoo.top`
- **数据在 exe 同级目录**（`config.go:97` `getProjectRoot`：settings.json / projects / Library / output）；安装器 per-user 装到 `{localappdata}\Programs\AI_MIDI`，AppId 固定 `{{0741ee2c-…}`，卸载保留用户数据文件

---

## 三、总体设计

### 3.1 三层音源体系（本次结构优化的主干）

```
① 声音管理（全局注册表，设置页新页签）
   registry.json = 唯一事实源：SF2 音源 / 采样包 / VST3 合成器 / VST3 效果器 / MM 插件
        │  添加·扫描·导入·启停·删除（含 AI 徽标、权限与哈希）
        ▼
② 输出机架（项目级乐器箱，存 arrangement.json）
   从注册表挑选音源进架；轨道与钢琴窗的可选音色只能来自机架
        │
        ▼
③ 使用点
   ├─ 编曲轨 source = 机架条目引用（+ 预设）
   ├─ 钢琴窗音色 = 机架条目引用（+ 预设）
   ├─ 素材库「音源」页签 = 注册表浏览（拖到轨道自动入架）
   └─ 混音台 FX 槽 / 素材库「效果器」页签 = 注册表中的效果器
```

原则：**注册表管"有什么"，机架管"这个项目用什么"，轨道/卷帘管"这一轨用什么"**。三处选择器共用同一个搜索/筛选组件。

### 3.2 数据模型与契约

#### 3.2.1 `Library/registry.json`（新增，声音管理注册表）

```jsonc
{
  "version": 1,
  "sources": [{
    "id": "sf2_generaluser",            // 稳定 id（前缀+uuid/短哈希）
    "kind": "sf2 | samplepack | vst3 | mm",
    "name": "GeneralUser GS", "vendor": "S. Christian Collins",
    "origin": "builtin | user | ai",     // ai → 设置页显示 AI 徽标
    "path": "Library/soundfonts/xxx.sf2",// sf2/vst3/mm；相对数据根或绝对
    "dirs": ["D:/samples/808"],          // samplepack 专用
    "category": "instrument | effect",   // vst3/mm
    "classUid": "VST3-xxxx",             // vst3
    "capabilities": ["file-read","network"], // mm：安装时需用户确认
    "hash": "sha256:…",                  // mm/vst3 记录用以校验
    "ai": { "author": "AI", "createdBy": "deepseek-v4-pro" },  // origin=ai 时
    "enabled": true, "tags": [], "addedAt": 0, "updatedAt": 0
  }]
}
```
- 后端新包 `internal/soundlib`：类型 + 存储（原子写、唯一临时名、进程内互斥）+ CRUD + 启动迁移（扫描 `Library/soundfonts/*.sf2` 生成 sf2 条目；`settings.MaterialDirs` 生成为"默认素材"采样包条目）
- IndexedDB 的角色降级：仅作浏览器模式（WebAudio）解码缓存，可按磁盘重建，不做迁移

#### 3.2.2 `arrangement.json` v2

```jsonc
{
  "version": 2,
  "bpm": 120, "loopRegion": …, "tracks": [ /* Track */ ],
  "rack": { "items": [ { "id":"rk_1", "sourceId":"sf2_generaluser",
                          "presetId":"0:0", "name":"钢琴", "pinned":true } ] },
  "mixer": { "channels": { "<trackId>": { "gain":0.8,"pan":0,"mute":false,"solo":false,
                                           "fx":[{"slot":0,"plugin":"vst3_xxx|mm_yyy","bypass":false,"wet":1.0}] } },
             "master": { "gain":1.0, "fx":[ … ] } },
  "pianoRoll": { "itemId":"rk_1", "presetId":"0:0" }
}
```
- `Track.source` 由 `{type,wave,tone,libId,presetId}` 改为 `{ "itemId": "rk_1", "presetId": "0:0" }`（波形/合成参数变为机架条目的预设属性）
- **v1→v2 迁移**（前端 `deserialize` 内做，读旧字段构造 rack 条目并回写）：synth → 内置"合成波"音源条目；builtin piano/strings → GeneralUser 预设条目；sf2{libId} → 按 libId 找注册表条目
- 插件二进制状态不进 JSON（5MB 上限）：存 `projects/{id}/plugins/track_<n>_slot_<m>.state|s0`
- 服务端新增 **arrangement ops API**（AI 与前端共用的定向修改入口，见 3.2.4）

#### 3.2.3 MM 插件包规范（`.mm` = zip，详见 P6 交付 `docs/MM插件规范.md`）

```
xxx.mm
├── manifest.json
├── dsp/win-x64/plugin.dll     # 按 MM SDK ABI 编译（v1 仅 Windows x64）
├── ui/index.html (+ assets)   # 可选；WebView 承载，消息桥接宿主
├── presets/*.json
├── docs/readme.md             # 可选（AI 与用户可读的使用说明）
└── LICENSE
```
manifest 关键字段：`formatVersion / id / name / version / author / origin("human"|"ai") / category(instrument|effect) / capabilities[] / params[{id,name,min,max,default,unit,group,ai:{description,hints}}] / commands[{id,name,description,params}] / ui{entry,size} / dsp{core:"native",abi:"mm-sdk/1",entry} / ai{usage,safetyNotes}`。
- **能力授权模型**：`capabilities` 在安装时列给用户确认；AI 创建插件若请求特权能力（文件/网络/线程），工具层弹出授权确认，默认拒绝
- **AI 可完全操作插件**：通用 `plugin_param_*` 工具（走 params），加 `commands`（插件自定义高层动作，如"扫频"、"载入 IR"）映射为工具调用
- **AI 徽标**：`origin:"ai"` 的插件在设置页声音管理列表中带专属 AI 标识（并与机架/素材库/参数面板/插件窗口标题统一出现，见 4.8）

#### 3.2.4 HTTP API 增量（`internal/server/router.go` 注册）

| 端点 | 用途 |
|---|---|
| `GET/POST/PUT/DELETE /api/sound/sources[/{id}]` | 注册表 CRUD、启停 |
| `POST /api/sound/import/sf2·mm` | 导入 SF2 / 安装 .mm（校验+哈希+能力确认） |
| `POST /api/sound/vst/scan` | 触发引擎 VST3 扫描（进度走 SSE 事件） |
| `GET /api/sound/samples?pack=` | 采样包内容列举（复用现有白名单前缀校验） |
| `POST /api/projects/{id}/arrangement/ops` | **定向修改**：addTrack/setTrackSource/setRackItem/setMix/setFxSlot…（服务端改状态 + SSE 通知前端） |
| `GET /api/plugin/params?track=&slot=`、`POST …/param`、`POST …/state` | VST/MM 参数与状态 |
| `POST /api/plugin/editor` | 打开/关闭原生插件编辑器窗口 |
| `POST /api/mm/build`、`POST /api/mm/install` | MM 一键编译 / 安装（供 AI 工具与开发者共用） |

#### 3.2.5 引擎 IPC v2 增量（`docs/引擎IPC协议.md` 同步修订）

- 协议版本升 **2**（主程序与引擎同包分发；启动握手校验版本，过旧即报"引擎版本不匹配"）
- 新增方法组：
  - `vst.scan{dirs[],rescan}` → 事件 `pluginScanProgress/Done`；`vst.list`
  - `plugin.load{track,slot?,kind:"instrument"|"effect",ref{path,classUid|mmId},state?}` / `plugin.unload{track,slot}` / `plugin.move?`
  - `plugin.listParams{track,slot}` / `plugin.setParam{...,paramId,value(normalized)}` / `plugin.getParam`
  - `plugin.getState` / `plugin.setState`（base64 或经文件路径，走 paths.go 白名单）
  - `plugin.openEditor{track,slot}` / `plugin.closeEditor`（引擎消息线程建窗）
  - `fx.setChain{track|master, slots[]}`（含 bypass/wet）
  - `mixer.setMaster{gain}`；`getLevels` 扩展返回 master 电平
  - `mm.load/unload`（或并入 `plugin.*`，按 ref.kind 分流）
- 引擎侧线程纪律：加载/卸载/建窗只在消息线程；音频线程只做原子指针交换 + 延迟释放队列（"trash queue" 消息线程回收）

#### 3.2.6 MCP 工具增量（AI 控制面，`internal/mcp/registry.go` + `executor.go`）

按功能归组，避免工具爆炸：

| 组 | 工具 | 说明 |
|---|---|---|
| 音源 | `list_sound_sources(kind?,q?)` | 读注册表（AI 先查有什么） |
| 机架 | `list_rack` / `add_rack_item(sourceId,preset?)` | 项目乐器箱 |
| 编曲 | `arrange_track(op:list/add/rename/remove)` / `set_track_instrument(track,rackItemId,preset?)` / `set_track_mix(track,gain?,pan?,mute?,solo?)` | 走 3.2.4 ops API，前端经 SSE 同步 |
| 混音台 | `mixer_get` / `mixer_set_channel(...)` / `mixer_set_fx(track\|master,slot,pluginRef?)` | |
| 插件 | `plugin_list_params(ref)` / `plugin_set_param(ref,paramId,value)` / `plugin_get_state` / `plugin_set_state` / `plugin_preset(op:list/save/load)` | VST 与 MM 共用 |
| MM 开发 | `mm_create_plugin(spec)` / `mm_write_file(path,content)` / `mm_build_plugin(dir)` / `mm_install_plugin(file)` / `mm_list` | 创建→编译→安装闭环；`mm_build_plugin` 首次调用需用户确认 |
| 节拍器/走带 | 复用现有前端能力，不加工具 | |

#### 3.2.7 SSE 事件增量

`arrangement`（前端重新拉取/热应用编排与混音台）、`sound`（注册表变更刷新设置页）、`pluginScan`（扫描进度）、`pluginEditor`（编辑器开窗状态）、`mmBuild`（编译进度日志）。前端在 `chat.ts` 事件分发处统一处理。

### 3.3 改名与数据迁移设计（P0 详情）

- **标识映射**：`AI_MIDI.exe→MelodyMate.exe`、`aimidi-engine.exe→melodymate-engine.exe`、Go module `aimidi→melodymate`、管道 `\\.\pipe\MelodyMate_ENGINE_<pid>`、互斥体 `Local\MelodyMate_*`、C++ 命名空间 `melodymate::*`、IDB `MelodyMate_SoundLibrary`、localStorage `melodymate-*`、日志 `output/melodymate.log`
- **更新地址**：新默认域名常量 + 保留旧 `aimidi-r2` 作为过渡期回退（旧安装要能发现新版本 → 旧桶持续提供指向新版安装包的清单；具体域名待你提供，见第七节）
- **数据迁移**（新 `internal/app/migrate.go`，启动时一次）：
  1. 若新数据根无 `settings.json` 且旧目录（`{localappdata}\Programs\AI_MIDI` 或同级 `AI_MIDI/`）有数据 → 复制 `settings.json / projects/ / Library/user/ / Library/soundfonts/ / output/`（复制不删，保留原目录可回滚）
  2. 前端 `migrateLegacyStorage()`（`app.ts` 早期调用）：`ai-midi-*→melodymate-*`、`aimidi_star_seen→melodymate_star_seen`；确认磁盘音源已进注册表后删除旧 IndexedDB
  3. 安装器：沿用同一 AppId（可识别升级）+ 新 `DefaultDirName`，`[Code]` 段杀旧进程名；卸载仍保留用户数据
- 首启提示一次"已从 AI_MIDI 迁移 N 个文件 / N 个音源"（toast + 日志）

---

## 四、前端体验与动效设计规范（新增界面详细规格）

> 本章是 P0–P6 前端交付的**设计基准**，与 `docs/前端设计必读.md`（物理动效哲学）和 `frontend/style.css` 设计令牌共同约束。
> 硬性要求：只用 `:root` 既有令牌（严禁硬编码颜色）、动画只用 `transform/opacity`、双主题（dark/parchment）逐屏走查、全部动效在 `prefers-reduced-motion: reduce` 下降级。

### 4.1 设计基线与复用/新建清单

**复用（不改样式语义）**：`.modal-overlay/.modal.card.brackets/.modal-title/.modal-actions`（modal-fly 双轨入场）、`.select-menu.menu-in`、`.ctx-menu`、`UI.toast(msg, kind)`、`.hud`+`hud-pop`、`.rack-window/.rack-filter-btn/.arr-rack-item`、`.settings-tabs/.settings-tab`、`.btn/.fn.pressed`、`.arr-panel-hint`、`.brackets` 装饰角、`arr-drop-ghost`、`spring-in/card-push/dyn-in-fly/sc-in-*` 关键帧。

**新建类名前缀约定**（延续现有 `arr-/pr-/rack-/settings-` 习惯）：
| 前缀 | 归属 | 例 |
|---|---|---|
| `snd-` | 声音管理（设置页）与通用音源选择器 | `.snd-source-card`、`.snd-badge-ai`、`.snd-picker` |
| `mat-` | 素材库三页签 | `.mat-tabs`、`.mat-item`、`.mat-wave` |
| `mxr-` | 混音台 | `.mxr-strip`、`.mxr-fader`、`.mxr-meter`、`.mxr-fx-slot` |
| `plg-` | 插件参数面板 / MM 承载窗 | `.plg-param-row`、`.plg-win` |

**信息密度与排版**：技术性文本（ID/数值/预设/路径/时间）一律 `Cascadia Mono`；名称与描述用正文字体；列表行高 30–36px，设置页行高 44px；所有新组件同时给出 hover / focus-visible / active / disabled / loading / empty 六态。

### 4.2 设置页「声音管理」页签（P1 交付）

```
┌ 声音管理 · SOUND LIBRARY ─────────────────────────────────────────┐
│ [🔍 搜索名称/厂商/标签…]   [类型 ▾]  [排序 ▾]        [＋ 添加 ▾]    │
│ ┌ 🎹 音源 SF2 · 12 ────────────────────────────────────────────┐ │
│ │ ▸ [SF2] GeneralUser GS   31MB · 128 预设 · 内置   ▷ 详情 ⋯   │ │
│ │ ▸ [SF2] PianoteqTest     99MB · 8 预设 · 用户     ▷ 详情 ⋯   │ │
│ ├ 📁 采样包 · 2 ───────────────────────────────────────────────┤ │
│ │ ▸ [PACK] 808 鼓组       3 个目录 · 412 文件 · 用户 ▷ 详情 ⋯  │ │
│ ├ 🎛 VST 插件 · 0（P5） / 🧩 MM 插件 · 1（P6）──────────────────┤ │
│ │ ▸ [MM] AI 立体声扩展  v1.0 · 作者 AI [AI] · 2 能力 ▷ UI ⋯   │ │
│ └──────────────────────────────────────────────────────────────┘ │
└───────────────────────────────────────────────────────────────────┘
```
- **分组卡**：每组一张 `card brackets` 卡（复用），组头 = 图标 + 名称 + 计数（monospace）+ 折叠箭头；组内空态给一行引导（如"尚未扫描 VST3 → 扫描"）。
- **条目卡 `.snd-source-card`**：左侧 kind 徽标（SF2=青 `--color-primary`、采样包=蓝、VST=紫、MM=橙 `--color-accent`；色值全部取自令牌或令牌混合，不新增色板）；中列主名 + 元信息行；右列 hover 浮出操作（▷ 试听 / 详情 / 停用开关 / ⋯ 菜单：打开位置、重新扫描、删除）。
- **AI 徽标 `.snd-badge-ai`**：橙描边胶囊 + ✨ 图标 + "AI"，`box-shadow` 呼吸光 `2s ease-in-out`（reduced-motion 下静态）；出现在设置页、机架、素材库音源卡、参数面板、插件窗口标题五处，形成统一识别。
- **详情弹窗**：复用 modal 系统；内容=路径、大小/哈希、预设数、能力清单（MM）、来源与添加时间、缩略信息；（MM/VST）"打开插件界面"按钮。
- **添加下拉**：上传 SF2 / 添加采样包文件夹 / 扫描 VST3（P5）/ 安装 .mm（P6），菜单 `.menu-in` 弹出手感。
- **动效**：切页签沿用 `settings.ts:1026 activatePane` 的方向动画；组内条目进场 `spring-in` + Stagger 40ms；删除确认走 modal 退场（modal-drop 0.20s）后条目 0.18s 淡出塌陷（用 `transform: scaleY` 关键帧，避免 height 动画）；新增条目落位 `spring-in`。
- **交互/无障碍**：搜索 200ms 防抖；↑↓ 选择、Enter 详情、Delete=删除（二次确认）；拖 SF2/.mm 文件到页签空白处 = 直接导入（drop 高亮 0.15s）；列表容器 `role="listbox"` 语义。

### 4.3 输出机架（乐器箱）与音源选择器（P2 交付）

**机架窗口改造**（保留 `.rack-window` 可拖拽浮窗形态，`chat.html:674-714`）：
```
┌ ● INSTRUMENT RACK · 乐器箱          ―  ✕ ┐
│ [全部][合成器][SF2][VST][MM][采样]  [＋ 加载音源] │
│ 🔍 搜索机架内音源…                                │
│ ┌────────────────────────────────────────────┐ │
│ │ ⋮⋮ [合成器] 合成波        ▷ 分配 ⋯        │ │
│ │ ⋮⋮ [SF2] GeneralUser GS › 钢琴  ▷ 分配 ⋯  │ │
│ │ ⋮⋮ [MM] AI 立体声扩展 [AI]      ▷ 分配 ⋯  │ │
│ └────────────────────────────────────────────┘ │
│ 已挂载 3 个音源 · 1 个 AI 生成      RACK READY │
└────────────────────────────────────────────────┘
```
- 分类页签沿用 `.rack-filter-btn`；「＋ 加载音源」替换现 `#arrRackAddSf2Btn`，样式用 `.rack-action-btn`（橙色描边）。
- 条目 = rack item：左侧 `⋮⋮` 拖把手（`cursor: grab`）、kind 徽标、名称+当前预设（monospace 小字）、hover 浮出 ▷试听 / 分配 / ⋯（移出机架、详情、打开插件界面）。
- 空态：居中图纸风插画位 + "乐器箱是空的" + 「从声音管理挑选音源」大按钮（点击开选择器）。

**音源选择器 `.snd-picker`（三处共用：机架 ＋ / 轨道头 / 钢琴窗）**：
```
┌ 选择音源 · PICK A SOURCE ──────────────────────────────┐
│ [🔍 搜索声音管理中的音源…]  [全部][合成器][SF2][VST][MM] │
│ ── 已在机架 ─────────────────────────────────────────  │
│ ● 合成波 (Synth)                            [已在架]    │
│ ● GeneralUser GS  ▸ 钢琴 (0:0)               [已在架]    │
│ ── 声音管理中的其他音源 ─────────────────────────────  │
│ ○ Serum (VST3 · 合成器 · 128 参数)           [＋ 入架]  │
│ ○ AI 立体声扩展 (MM · AI 生成) [AI]          [＋ 入架]  │
│ ↑↓ 选择 · Enter 确认 · Esc 关闭 · Tab 切页签            │
└─────────────────────────────────────────────────────────┘
```
- 预设二级视图：选中带预设的音源 → 列表右侧滑入预设列（`sc-in-right` 0.24s Natural Spring），顶部面包屑可返回；SF2 预设量大时带搜索与虚拟滚动。
- 试听：条目行内 ▷，播放时按钮变脉冲态（`rackPulse` 复用），再次点击停止；试听走引擎 29 轨。
- 拖拽链路：条目 dragstart 生成拖影（复用 `arr-drop-ghost`：音源名 + kind 徽标）；机身轨道头上悬停高亮（描边 + 色条），拖到空白区 = 新建轨并分配；drop 后 toast「已分配到 轨道 N」。
- 动效：机架打开走 `dyn-in-fly` + 条目 Stagger 40ms；入架成功条目 `spring-in`；移出机架条目 0.18s 收拢淡出；选择器若以模态打开，复用 modal-fly 双轨（0.40s/0.50s）。

### 4.4 右侧素材库（三页签，P3 交付）

```
┌ 素材库 · MATERIAL ────────────────┐
│ [采样] [音源] [效果器]     🔍 搜索  │
│ ┌───────────────────────────────┐ │
│ │ ▁▃▅▇ wav  kick_808.wav        │ │  ← 波形缩略图 + 名称
│ │      0:01.2 · 44KB · 808鼓组   │ │  ← 时长/大小/所属包（mono）
│ │ ─────────────────────────────  │ │
│ │ ▁▂▄▆ snare_top.wav            │ │
│ └───────────────────────────────┘ │
│ ⇱ 拖拽音频到轨道创建 Clip           │
└───────────────────────────────────┘
```
- **页签 `.mat-tabs`**：segmented 三态（采样/音源/效果器），选中态用 `--color-primary-soft` + 描边；切换时内容 0.22s 上滑淡入 + 条目 Stagger 30ms；搜索框跨页签保留关键词。
- **采样页签**：保留文件夹树（展开态 localStorage 记忆），每条目增加波形缩略（首帧峰值 canvas，尺寸 48×22，缓存于内存 Map）、时长/大小/采样率（monospace）、所属采样包标签；悬浮 ▷ 试听；拖拽语义不变（→ 音频 Clip，落点小节标注沿用现有剪影）。
- **音源页签**：卡片网格（自适应 2 列，窄栏回落 1 列）：kind 徽标 + 名称 + 预设摘要 + `[已在架]/[＋ 入架]` 状态胶囊 + AI 徽标；拖到轨道 = 分配（自动入架）；双击 = 试听；右键 = 入架/移出/详情/在声音管理中显示。
- **效果器页签**：列表卡（kind 徽标 + 名称 + 参数数）；P5/P6 前为引导空态「效果器将装载到混音台 FX 槽（P4/P5 就绪后可用）」；就绪后拖到 `.mxr-fx-slot` 高亮提示。
- **显示优化要点**：行高 30px、悬停整行高亮（`--color-bg-alt`）、长名省略号 + title、超过 200 条启用虚拟滚动、空目录给"查看位置/重新扫描"操作；双主题逐一走查（暖纸下波形色 `--color-primary`、暗蓝下 `--color-hud`）。

### 4.5 混音台（P4 交付）

```
┌ MIXER · 混音台 ─────────────────────────── [通道宽度▾] [×] ┐
│ ┌ 1 钢琴 ┐┌ 2 贝斯 ┐┌ 3 鼓 ┐   …   ┌ MASTER ─┐           │
│ │ ▮▮▮▮▮  ││ ▮▮▮▮▮ ││ ▮▮▮▮ │       │ ▮▮▮▮▮▮ │  ← 推子   │
│ │ -6.0dB ││  0.0dB ││ -3.2 │       │  0.0dB  │           │
│ │  ◑ 声像 ││  ◑     ││  ◑   │       │         │           │
│ │ [M][S] ││ [M][S] ││[M][S]│       │         │           │
│ │ FX1 ＋  ││ FX1 ＋  ││ FX1 ＋│       │ FX1 ＋  │           │
│ │ FX2 ▸Ser││ FX2 空  ││ FX2 ＋│       │ FX2 ＋  │           │
│ │ ▮▮ 电平 ││ ▮▮     ││ ▮▮   │       │ ▮▮▮▮    │           │
│ └────────┘└─────────┘└───────┘       └─────────┘           │
└─────────────────────────────────────────────────────────────┘
```
- **入口**：编曲窗顶栏「🎛 混音台」按钮 + `F9`；与编曲窗同一容器内**底部升起面板**（`Fluid Glide 0.38s cubic-bezier(0.16,1,0.3,1)`，收起 `Snappy Collapse 0.24s`），非独立窗口——保证与时间线同屏对照。
- **通道条 `.mxr-strip`**（宽 76px，主通道 92px）：顶部色条取自轨道色（8 色预设，与轨头一致）、名称（双击改名，与轨头联动）、推子（自绘：`input[type=range]` 隐藏 + 轨道槽 + 滑块，双击=0dB，Alt+拖=精调，滚轮=±1%）、声像旋钮（拖动水平调，双击回中）、`[M][S]`（与轨头状态互斥同步、solo 逻辑同现有 anySolo）、FX 槽列表、双条电平表。
- **严格 1:1**：通道条数量 = 编曲轨数量，增删轨道即时增删通道（新增 `spring-in` + 邻位 `card-push` 让位）；不允许隐藏/绕过通道；主通道固定最右、独立样式（母带链 + 总线电平 + 总增益）。
- **电平表 `.mxr-meter`**：`getLevels` 30Hz 轮询；每帧只改 `transform: scaleY`；峰值保持 1.2s 后 0.6s 缓降；>-0.5dBFS 顶部红闪（`--color-record`）；停止播放 0.5s 内自然归零。
- **FX 槽 `.mxr-fx-slot`**：空槽=虚线框 + ＋（拖拽悬停高亮）；占用=插件名 + 电源/bypass 小开关 + 双击打开参数面板；拖拽排序显示插入位提示线；卸载经 ⋯ 菜单（二次确认）。
- **选中联动**：点击通道条 = 选中对应编曲轨（轨头高亮 0.6s 脉冲）；双击通道名 = 定位到轨道。
- **无障碍**：推子/声像 `role="slider"` + aria-valuenow/valuetext；键盘 ←→ ±1%、Shift ±10%、Home/End 极值；M/S 可 Tab 聚焦。

### 4.6 插件参数面板与原生编辑器伴随 UI（P5 交付）

- **通用参数面板 `.plg-param-panel`**（轨道音源 / FX 槽共用）：从槽位/轨头按钮展开（0.24s Fluid Glide 高度由 `scaleY` 关键帧模拟）；结构 = 头部（插件名 + 厂商 + AI 徽标 + 预设下拉 + 「🖥 打开插件界面」+ 随机/重置）、参数分组折叠列表（参数名、滑杆、数值 monospace、单位、右键菜单预留 MIDI Learn/复制值）。
- 参数下发 30Hz 节流 + 引擎侧平滑；面板内改动 0.2s 高亮回显（引擎回读值）。
- **原生编辑器窗口**（引擎侧 JUCE 窗口）：前端只维护状态——按钮显示"打开/关闭界面"；窗口标题 `插件名 · MelodyMate`；引擎重启/插件卸载导致窗口消失时，前端 toast（warn）+ 按钮复位。
- 无编辑器的插件：按钮禁用并 tooltip「该插件未提供界面，请使用参数面板」。
- **AI 操作回显**：AI 经工具改参数 → 对应参数行 `hud-pop` + 0.6s 高亮脉冲（可在设置里关闭"AI 操作可视反馈"）。

### 4.7 MM 插件 UI 承载窗（P6 交付）

- 载体页 `frontend/mm-ui.html?plugin=<id>`：窗口 chrome 完整复用 `.rack-window` 语言（标题栏：● 状态点 + 插件名 + 作者徽标（人类/AI）+ ―/✕；右下角缩放把手；可拖拽/贴边）。
- 顶部宿主工具条（插件不可伪造/覆盖）：预设下拉、参数表按钮（打开 4.6 的参数字典）、「AI 可操作」说明气泡。
- 插件 UI 运行在 `sandbox="allow-scripts"` 的 iframe（无 `allow-same-origin`），经 postMessage 桥访问宿主 API（参数读写/状态/走带/尺寸上报）；iframe 内禁用外部导航；CSP 与主页面一致。
- 动效：窗口打开 modal-fly；插件内调参 → 宿主参数面板同步行高亮 0.2s；UI 加载中显示骨架 shimmer（`card-loading` 复用）。

### 4.8 AI 相关界面（P5/P6 交付）

| 界面 | 设计 |
|---|---|
| AI 徽标 | `.snd-badge-ai` 统一五处出现（4.2 节），呼吸光在 reduced-motion 下静态 |
| 能力授权对话框（安装/编译 MM） | modal：作者（AI）+ 能力清单（图标+说明）+ 哈希 + 风险说明；主按钮「授权并安装」（橙色 `.btn-primary`）、次按钮「取消」；拒绝 = 工具层返回可解释错误给 AI |
| 一键编译进度 | 对话流内卡片（复用 `q-card` 样式族）：阶段步骤条（配置→编译→打包→校验）+ 折叠日志 + 失败一键复制；HUD 显示"编译中" |
| AI 改动回显 | 机架/轨道/混音台/参数面板收到 SSE `arrangement/plugin` 事件后，对应控件 `hud-pop` + 0.6s 脉冲高亮 |
| VST/MM 扫描进度 | 设置页内联进度（不确定态 shimmer + 已发现计数滚动递增） |

### 4.9 品牌视觉刷新（P0 交付）

- 新标识设计（延续"工程图纸/蓝图"美学：网格底 + 青主色 + 橙强调）：产出 `app_icon.ico`、`app_icon_dark/warm.ico`、`window_icon.ico`、`splash.png/dark/warm`、`favicon*`、titlebar/关于页/免责声明/Star 弹窗内嵌 SVG。
- 文案替换清单（用户可见）：窗口标题 `MelodyMate · AI 编曲助手`、三页 `<title>`、关于页名称/标语/链接、免责声明与 Star 弹窗、安装器显示名与快捷方式名、README×2。
- 动效不新增：沿用启动图淡出、标题栏按钮 `fn-press`、关于页特效（`initAboutFx`）既有规范。

### 4.10 动效对照总表（新组件）

| 组件/事件 | 动画 | 曲线（见设计必读） | 时长 | reduced-motion |
|---|---|---|---|---|
| 设置页条目/组进场 | spring-in + Stagger 40ms | Natural Spring | 0.40s | 直接显示 |
| 音源选择器弹窗 | modal-fly + modal-size 双轨 | Fluid Glide | 0.40/0.50s | 淡入 |
| 选择器预设列滑入 | sc-in-right | Natural Spring | 0.24s | 淡入 |
| 机架条目入架/新增 | spring-in | Bouncy Pop | 0.35s | 静态 |
| 机架条目移出 | 收拢淡出（scaleY） | Snappy Collapse | 0.18s | 瞬时 |
| 素材库页签切换 | 内容上滑淡入 + Stagger 30ms | Natural Spring | 0.22s | 淡入 |
| 混音台面板开合 | 底边升起/落回 | Fluid Glide / Snappy | 0.38/0.24s | 瞬时 |
| 混音台新增/删除通道 | 邻位 card-push + spring-in | Natural Spring | 0.55s | 静态 |
| FX 槽装载/旁通 | fn-press + 名称淡入 | Bouncy Pop | 0.35s | 静态 |
| 参数面板展开 | 高度 scaleY 展开 | Fluid Glide | 0.24s | 瞬时 |
| AI 操作回显 | hud-pop + 高亮脉冲 | Bouncy Pop | 0.32s/0.6s | 静态 |
| 插件 UI 窗口 | modal-fly | Fluid Glide | 0.40s | 淡入 |
| 电平表 | 每帧 transform: scaleY | —（非插值动画） | 30Hz | 不变 |

**前端验收统一口径**（每个里程碑均执行）：① 双主题逐屏截图走查（暗蓝 + 暖纸，含 hover/focus/空态/加载态）；② 动效录屏对比曲线与时长表；③ `prefers-reduced-motion` 下无位移/缩放动画；④ 键盘可达性（Tab 顺序、Esc 分层关闭、↑↓/Enter 列表操作）；⑤ 与既有组件（模态/菜单/HUD/Toast）手感一致性抽查。

---

## 五、分阶段实施

### P-1 · 分支与工作区整理（动工前第一步，必须最先做完）

改动量大且跨两个仓库，先隔离工作区，避免污染 `Optimize` 主线：

1. **主仓库**（当前 `Optimize` @ `bbdfe54`，工作区已干净）：
   - 创建并切换到新分支：`git switch -c feat/melodymate`
   - 里程碑粒度提交（`P0 改名` / `P1 声音管理` / … 各一组提交，message 沿用仓库现有中文约定 `feat(scope): …`）
   - 每阶段验收通过后再进入下一阶段；阶段性成果按你的节奏合并回 `Optimize`（不做自动合并）
2. **引擎仓库**（`engine/`，独立 git，当前 `master` 有**未提交改动**：`Source/Audio/{EngineAudioDevice,MidiRing,SamplePool,SynthEngine,Transport}.{cpp,h}`、`Source/Ipc/PipeServer.cpp`、`Source/Main.cpp`、`ThirdParty/JUCE` 子模块指针）：
   - 先确认这批进行中改动完成度并提交到 `master`（或按你的判断处理），再创建 `feat/melodymate-engine` 分支
   - 每次引擎改动：引擎版本号 +1 → 构建 `melodymate-engine.exe` → 拷入主仓库 `bin/` → 同步 `docs/引擎IPC协议.md`
3. 主仓库侧的 `.gitignore` 已忽略 `/engine/`，两仓库各自独立提交，不互相引用 commit id（用版本号与协议文档对齐）

> 每阶段结束必须：`gofmt -l .` 干净、`go vet ./...`、`go test ./... -count=1`、`go test -race ./internal/...`、`npm run build`（前端 tsc + 擦除）、桌面 `go build -tags desktop,production` 通过；涉及引擎的阶段追加引擎构建 + `tools/engine_smoke` 冒烟。

### P0 · 品牌改名 MelodyMate（含内部标识与数据迁移）

**目标**：全仓库（含引擎）改名完成，老用户数据无损迁移，自动更新路径不断。

1. 主仓库机械改名：`go.mod` 模块名与全部 import（`internal/**`、`tests/**`、`tools/**`）；`config.go` WindowTitle/日志名；`wails.json`；三个 HTML（title/关于页/免责声明/Star 弹窗/titlebar）；`app.ts/theme.ts` 注释与存储键；`chat.ts` 仓库 URL；`settings.ts` issues URL。
2. 引擎仓库：CMake target/`COMPANY_NAME`、`namespace aimidi→melodymate`、管道名与互斥体、日志前缀；构建产物更名 `melodymate-engine.exe` 拷入 `bin/`；`supervisor.go:ResolveEnginePath` 同步。
3. 构建与发布脚本：`RUN.bat`、`quick_build.bat`、`tools/do_build.bat`、`assemble_buildbin.bat`、`count_project.bat`、`AI_MIDI.iss→MelodyMate.iss`（AppId 不变）、`.github/workflows/*`、`dist/release` 脚本。
4. `internal/app/migrate.go` + 前端 `migrateLegacyStorage()`（按 3.3）。
5. 配置常量：`UpdateManifestURLs` 有序回退（新→旧），文档更新（README×2、发布指南、docs 内引用）。
6. **品牌视觉刷新（前端）**：按 4.9 节产出新标识全套资源（图标 4 件套 / 启动图 3 件 / favicon / 内嵌 SVG）与用户可见文案替换；关于页、免责声明、Star 弹窗、标题栏同步改版；动效沿用既有规范不新增曲线。

**验收**：老版本安装目录（含 settings/projects/Library/user/自定义 sf2）升级后全部可用；`git grep -i "ai_midi\|aimidi"` 仅剩迁移/兼容代码与历史文档；安装包升级路径实测通过（旧版→新版，设置与项目保留）；新品牌资源在双主题 + 高 DPI 下清晰无锯齿，启动图与标题栏动效与旧版手感一致。

### P1 · 音源注册表 + 设置页「声音管理」

**目标**：SF2/采样包集中注册管理，成为全局唯一音源事实源。

1. 后端 `internal/soundlib/`：模型、存储、CRUD、启动迁移（扫 `Library/soundfonts`、`MaterialDirs`）。
2. `internal/server/handler_sound.go` + 路由：`/api/sound/sources*`、`/api/sound/samples`；沿用现有路径白名单与上传上限校验（`handler_arrangement.go:109-141`、`handler_soundfont.go` 的魔数/大小校验）。
3. 设置页新页签「声音管理」（`settings.html` + `settings.ts`，复用 `.settings-tabs` 模式，`settings.ts:1026` activatePane）：分组=音源(SF2) / 采样包 / VST 插件 / MM 插件（后两组 P5/P6 填充），支持搜索、启停、删除、AI 徽标位置预留、空态引导。
4. **前端界面按 4.2 节规格实现**：分组卡（`card brackets`）+ 条目卡 `.snd-source-card` 六态 + kind 徽标配色 + AI 徽标 `.snd-badge-ai`（呼吸光）+ 详情弹窗 + 添加下拉菜单 + 拖文件导入高亮 + 条目 Stagger 40ms 进场与删除收拢动效 + 键盘操作（↑↓/Enter/Delete）与 `role=listbox` 语义。
5. 前端 `frontend/src/soundlib.ts`（全局 `SoundRegistry`）：列举/搜索/过滤/图标/徽标统一出口；**替换**机架与钢琴窗里的散列枚举（`arrange.ts:3805-3849`、`pianoroll.ts:3633-3654`），IndexedDB `SoundLibrary` 降级为浏览器模式解码缓存。
6. SF2 上传入口统一收敛：钢琴窗弹窗与机架导入改为调用注册表 API，设置页可上传。

**验收**：设置页可增删启停 SF2/采样包；机架与钢琴窗仍可正常发声（内部已走注册表）；浏览器模式（WebAudio）解码缓存仍工作；老用户旧文件自动入册；4.2 节前端验收五项（双主题截图/动效对照/降级/键盘/手感一致性）全过。

### P2 · 输出机架 → 乐器箱（含 AI 编曲操作 API）

**目标**：机架成为项目乐器箱；轨道与钢琴窗音色只能来自机架；AI 可经工具操作轨道与机架。

1. 数据层：`arrangement.json v2`（rack/pianoRoll/mixer 字段 + track.source 改引用）+ 前端 `deserialize` v1→v2 迁移（`arrange.ts:395`）。
2. 服务端 ops API：`POST /api/projects/{id}/arrangement/ops`（`internal/server/handler_arrangement_ops.go`，新增：加锁读改写 + 校验 + SSE `arrangement` 事件）；AI 工具与前端共用同一实现。
3. 机架重写（`arrange.ts:3602-4100` 拆到新文件 `frontend/src/arrangement/rack.ts`）：条目=rack items（按 kind 分组：合成器/SF2/VST/MM/采样），去掉全部硬编码；`#arrRackAddSf2Btn`→「＋ 加载音源」；**按 4.3 节实现**机架窗口（分类页签/搜索/条目六态/拖把手/空态）+ **音源选择器 `.snd-picker` 弹窗**（已在架/其他音源两段、预设二级视图滑入、行内试听脉冲、键盘 ↑↓/Enter/Esc、三处共用同一组件）+ 拖影与落点高亮；条目右键（移出/试听/详情/打开插件界面）；采样组保留并接采样包。
4. 轨道音源：`setTrackSource/replaceTrackSourceFromDrop`（`arrange.ts:2721/2907`）改吃机架条目；轨头 `openSourceMenu`（`arrange.ts:2670-2715`）改为机架选择器；`audio_engine.ts:192-274` 的 `ensureTrack` key 体系改为 `sourceId:presetId`；拖拽 hover 高亮与 drop toast 按 4.3 节。
5. 钢琴窗：音色下拉（`pianoroll.ts:3150-3660`）改为机架选择器（含预设级联），选择持久化到 `arrangement.pianoRoll`；引擎轨 30/31 逻辑保留。
6. AI 工具：`list_rack/add_rack_item/set_track_instrument/arrange_track`；前端在 `chat.ts` 事件分发新增 `arrangement` 事件处理（拉取并热应用，不打断播放），AI 改动按 4.8 节做 `hud-pop` 回显。

**验收**：机架内所有音源均可试听与分配；轨道/钢琴窗选择器只能选机架条目；删/增机架条目、AI 工具改轨道音源后 UI 与声音即时一致；v1 工程打开自动迁移无感；4.3 节前端验收五项全过。

### P3 · 素材库改版（采样 / 音源 / 效果器 三页签）

**目标**：右栏信息更清晰、分类分页、搜索可用。

1. HTML/CSS 重构（`chat.html:353-361` + `style.css:5673/6183-6275`），**按 4.4 节规格实现**：`.mat-tabs` segmented 页签（选中态/内容上滑淡入+Stagger 30ms）、跨页签保留的搜索框、条目行六态、空态与骨架屏、>200 条虚拟滚动。
2. 采样页签（现有能力保留并优化）：文件夹树 + 采样包分组、**波形缩略图** + 时长/大小/采样率（monospace）、拖拽成 clip（沿用落点小节剪影）、双击试听。
3. 音源页签：注册表音源（SF2/VST/MM）卡片网格 + `[已在架]/[＋ 入架]` 状态胶囊 + AI 徽标；拖到轨道=分配并自动入架；双击=试听；右键=入架/移出/详情。
4. 效果器页签：注册表效果器列表（P4/P5 前为引导空态）；就绪后拖到 `.mxr-fx-slot` 高亮提示。
5. `arrange.ts` 素材部分抽到 `frontend/src/arrangement/material.ts`。

**验收**：三页签皆可搜索过滤；拖拽语义正确（采样→clip、音源→轨道、效果器→FX 槽或提示）；波形缩略图在双主题下清晰；4.4 节前端验收五项全过。

### P4 · 混音台 v1（通道条 + FX 基础设施）

**目标**：FL 风格混音台，与编曲轨自动 1:1；打通 FX 链引擎基础设施（FX 槽先空置）。

1. 引擎：`MixerGraph` 增加每轨/主总线 FX 槽数组与处理位点（track synth → inserts → strip → master inserts → limiter，`EngineAudioDevice.cpp:932-991`）；`fx.setChain`、`mixer.setMaster`、master 电平；加载/卸载消息线程 + 音频线程原子交换 + 延迟释放队列。
2. Go：supervisor 增加 `lastChains` 记录与重放（顺序在 setTrackVoice 之后）、`SetFxChain/SetMaster/GetLevels(master)`；`types.go` 新参数结构。
3. 前端 `frontend/src/arrangement/mixer.ts`，**按 4.5 节规格实现**：编曲窗内底部升起面板（F9/顶栏按钮，`Fluid Glide 0.38s` 开、`Snappy 0.24s` 收）；通道条 `.mxr-strip`（色条/名称双向联动轨头、自绘推子双击归零 + Alt 精调 + 滚轮 ±1%、声像旋钮、M/S、FX 槽、双条电平表）；主通道固定最右；增删轨道即时增删通道（`spring-in` + 邻位 `card-push`，严格 1:1 不允许隐藏）；电平表每帧仅 `transform: scaleY` + 峰值保持 + 过载红闪；推子/声像 `role=slider` 键盘可达。
4. 持久化：`arrangement.mixer`；`deserialize`/`serialize`（`arrange.ts:331-371/395-540`）双向打通；撤销/重做入栈（`applyMixSafe` 链路 `arrange.ts:1467-1540`）。
5. AI 工具：`mixer_get/mixer_set_channel`（走 ops API），AI 改动按 4.8 节回显。

**验收**：播放中改推子/声像/静音即时生效且与轨头一致；电平表实时、过载指示准确；刷新/重开工程/引擎重启后混音状态完整恢复；AI 工具调整后 UI 同步高亮；4.5 节前端验收五项全过。

### P5 · VST3 宿主（乐器 + 效果器 + 原生编辑器 + AI 参数控制）

**目标**：扫描、管理、加载、调参、开原生界面、状态随工程保存。

1. 引擎构建：`CMakeLists.txt` 追加 `juce_audio_processors`、`juce_gui_basics`，定义 `JUCE_PLUGINHOST_VST3=1`；`PluginHost.h` 转为 `PluginHost.{h,cpp}` 进编译（扫描线程、黑名单、已知清单缓存 `output/vst_cache.xml`）；GUI 初始化（控制台程序 + `ScopedJuceInitialiser_GUI`/消息线程）。
2. 引擎音频：轨乐器槽支持 VST3（替代 tsf/wave 声源，与 `setTrackVoice/loadSoundFont` 互斥）；FX 槽支持 VST3 效果（P4 基础设施接入）；`processBlock` 用 MidiBuffer 喂事件；尾音/延迟补偿按插件 latency 处理（v1 记录不补偿，文档注明）。
3. IPC（3.2.5）：`vst.scan`（进度事件）、`plugin.load/unload/listParams/setParam/getParam/getState/setState/openEditor/closeEditor`、`fx.setChain` 扩展支持插件引用。
4. Go：`internal/plugins/`（插件引用解析、状态文件读写 `projects/{id}/plugins/*.state`、注册表写回 classUid/类别/厂商）；supervisor 重放链扩展为「音源 → FX 链 → 编辑器窗口按需恢复」。
5. 前端（**按 4.6/4.8 节规格实现**）：设置页「声音管理·VST 插件」分组（扫描按钮 + 内联 shimmer 进度 + 已发现计数滚动 + 添加/移除 + 条目详情含厂商/类别/参数数/路径）；机架与素材库音源/效果器页签接 VST（kind 徽标=紫）；**通用参数面板 `.plg-param-panel`**（分组折叠参数行、30Hz 节流、引擎回读 0.2s 高亮、预设下拉、随机/重置、`role=slider` 键盘可达）+ 「🖥 打开插件界面」按钮与原生窗口状态同步（窗口消失→toast 提示 + 按钮复位）；混音台 FX 槽 → 参数面板/原生编辑器；AI 改参数按 4.8 节 `hud-pop` 回显。
6. AI 工具：`vst_list_plugins`（并入 `list_sound_sources`）、`plugin_list_params/plugin_set_param`（VST 与 MM 共用一套），轮询/事件回报参数值。

**验收**：安装一台真实 VST3 合成器与一台效果器：扫描→添加→分配到轨/FX 槽→发声/处理→改参数→开原生窗口→保存工程→重启应用→状态完整恢复；插件崩溃时引擎自动重启且主程序给出提示；AI 通过工具改参数实测生效；参数面板与原生窗口在 100%/200%/300% DPI 下均可用，4.6 节前端验收五项全过。

### P6 · MM 插件平台（规范 + SDK + 一键编译 + 引擎运行时 + AI 闭环）

**目标**：.mm 格式从规范到工具链到运行时的完整落地，AI 可自写插件并被识别。

**平衡点（设计原则）**：DSP 走**原生 C++ 编译**（与 VST 同一条引擎音频链路，性能同级）；插件作者只写"插件逻辑 + UI"，音频输入输出/线程/缓冲/参数同步全部由 SDK 与宿主托管——AI 编写时默认只接触安全面，需要触碰特权能力必须显式授权；产出经一键编译打包为 .mm。

1. **规范**：`docs/MM插件规范.md`（包结构、manifest 完整 schema、能力模型、参数与预设、UI 桥协议、ABI、版本与兼容策略、安全与信任模型：DLL=本地代码，安装需用户确认能力清单 + 哈希记录 + 引擎受监管重启）。
2. **SDK**（引擎仓库 `engine/mm-sdk/`）：C++17 头文件 + CMake 模板；宿主托管音频 I/O（`processBlock(AudioBufferView, MidiView)`）、参数存储（原子 + 平滑）、状态序列化、走带信息；**默认不暴露**文件/网络/线程 API，声明 `capabilities` 且用户授权后才在编译期（`MM_CAP_FILES` 等宏）与运行期（宿主校验）开放；提供 `MM_DECLARE_PLUGIN` 等宏与参数声明 DSL。
3. **一键编译工具**（`cmd/mm/`，构建为 `mm.exe`）：`mm new`（模板脚手架，含 UI 骨架）/ `mm doctor`（检测 VS2022 Build Tools + cmake，缺失给安装指引）/ `mm build`（配置+编译+收集+校验 manifest+打包 zip+sha256）/ `mm validate` / `mm inspect` / `mm install`（写注册表）/ `mm test`（离线渲染自测，复用引擎 bounce 或本地渲染）。
4. **引擎运行时**：安装目录 `Library/plugins/<id>@<ver>/`（解包 + 哈希校验）；`plugin.*` IPC 按 `ref.kind=mm` 分流；乐器（轨音源）与效果（FX 槽）两种宿主；UI 页面（宿主提供 `mm-ui.html`，沙箱 iframe + postMessage 桥：参数读写/状态/走带/尺寸）。
5. **前端（按 4.7/4.8 节规格实现）**：设置页「声音管理·MM 插件」分组（安装/卸载/能力与哈希展示/**AI 徽标**/详情/打开 UI）；机架与素材库接入（kind 徽标=橙）；**插件 UI 承载窗**（`frontend/mm-ui.html`：复用 `.rack-window` chrome + sandbox iframe + postMessage 桥 + 宿主工具条 + 尺寸上报 + 骨架加载态）；**能力授权对话框**与**一键编译进度卡片**按 4.8 节实现。
6. **AI 闭环**：`mm_create_plugin`（脚手架+写入代码）→ `mm_write_file` → `mm_build_plugin`（首次需用户确认，返回编译日志）→ `mm_install_plugin` → `plugin_list_params/plugin_set_param`、`commands` 映射的自定义工具 → AI 生成的插件在声音管理显示 AI 徽标。
7. **开发者文档与示例**：`docs/MM插件开发指南.md`（SDK 参考 + 从零到打包完整教程）；`examples/mm/gain`、`examples/mm/filter`、`examples/mm/synth` 三个示例（含 UI）+ 模板目录；示例作为 `mm doctor/build` 的回归用例。

**验收**：① 人类开发者按文档用 `mm new/build/install` 从零做出可发声插件；② AI 在对话中创建一个小合成器/效果器插件，一键编译安装成功后出现在声音管理中（带 AI 徽标）并可被加载、调参、开 UI；③ 未授权能力（如网络）被工具层与宿主双重拒绝；④ 插件崩溃不影响主程序（引擎受监管重启）；⑤ AI 生成插件的识别链路（徽标/授权对话框/编译进度卡）在双主题下完整可用，4.7/4.8 节前端验收五项全过。

### P7 · 全量回归、文档与发布

1. 回归：`go test ./... -count=1`、`-race`、E2E 正向/负向脚本（`build/test/e2e_*.py` 扩展新端点）、`tests/run_e2e.go`、浏览器 UI 全流程、桌面 exe 冒烟（含引擎、VST、MM）。
2. **前端终检**：按 4.10 节口径对全部新增/改造界面做双主题截图集 + 动效录屏 + reduced-motion + 键盘可达 + 高 DPI 五项终检，产出走查报告（问题清零）。
3. 迁移回归：v3.1.x 老工程/老安装目录 → 新版全链路（设置/项目/知识库/音源/机架/混音台/插件）。
4. 文档：README×2、`docs/引擎IPC协议.md`(v2)、`docs/编曲窗口设计文档.md`（机架/混音台/素材库）、`docs/MM插件规范.md`、`docs/MM插件开发指南.md`、`docs/发布指南.md`（改名迁移与双域名过渡）、新增 `docs/声音管理设计.md`、新增 `docs/前端新增界面走查报告.md`。
5. 发布：`wails build` + 引擎构建 → `MelodyMate.iss` 打包 → 旧域名过渡清单 → Release 说明（含"从 AI_MIDI 升级"指引）。

**验收**：审计级全绿（对照 `docs/audit-2026-09-25.md` 的执行口径）；安装包在干净机器上从旧版升级成功。

---

## 六、测试与回归策略

- 单测：`internal/soundlib`（注册表 CRUD/迁移/白名单）、`internal/plugins`（引用解析/状态文件）、ops API（并发与校验）、MM 包校验（畸形 zip/manifest）
- 引擎侧：`tools/engine_smoke` 扩展 VST/MM 用例；`PluginHost` 扫描/实例化失败路径；FX 链加载卸载的音频线程安全（跑 bounce 同时切链）
- 前端功能：`npm run typecheck` + `node --check` 全 js；浏览器 UI 自动化（设置页/机架/素材库/混音台/插件面板全流程）
- **前端视觉与动效（每里程碑必做，按第四章 4.10 口径）**：双主题逐屏截图（含 hover/focus/disabled/空态/加载态/错误态）；动效录屏比对曲线与时长表（Bouncy Pop / Fluid Glide / Natural Spring / Snappy Collapse 四曲线不得混用错位）；`prefers-reduced-motion` 全量降级验证；键盘可达性与焦点顺序；与既有组件手感一致性抽查；高 DPI（100/200/300%）下图标与文字清晰度
- 安全负向：能力未授权拒绝、恶意 manifest、路径穿越（插件状态/采样/MM 解包目录）、超大/畸形 .mm、插件扫描目录越界
- 性能：30 轨 × VST 效果器的 CPU/内存水位；MM 原生 DSP 与 VST 基准对比；电平表 30Hz 下的渲染帧率（≥55fps）；SF2 多轨内存（评估 `tsf_copy` 共享优化，列为可选优化项）

## 七、风险与对策

| # | 风险 | 对策 |
|---|---|---|
| R1 | 改名破坏老用户自动更新（exe/目录/域名全变） | AppId 不变 + 首启迁移 + 旧清单域名过渡期继续服务；发布说明明确升级路径 |
| R2 | VST 插件崩溃/卡死拖垮引擎 | 引擎受监管重启（已有熔断）+ 扫描超时/黑名单 + 崩溃归因提示；进程隔离列为后续增强 |
| R3 | 原生编辑器窗口的 DPI/焦点/生命周期 | 引擎消息线程统一管理窗口；引擎重启即关窗并提示；常规 DPI 与 300% DPI 双实测 |
| R4 | MM 需本机 C++ 工具链（VS2022 Build Tools） | `mm doctor` 检测 + 引导安装；文档明示前置条件；后续可评估随包 clang 方案 |
| R5 | DLL = 任意本地代码的信任问题 | 安装时能力清单 + 用户显式确认 + 哈希记录 + AI 徽标 + 引擎受监管；不做静默安装 |
| R6 | 插件二进制状态体积（工程膨胀） | 状态独立文件 + 上限与清理入口；arrangement.json 保持小而可读 |
| R7 | 工具数量膨胀影响 LLM 工具选择 | 归组（3.2.6），描述精炼；系统提示词加"何时用哪组"的指引 |
| R8 | 32 轨上限与 29/30/31 保留轨位 | 混音台只映射实际编曲轨；轨位约定写入文档并在越界时明确报错 |

## 八、需要你提供/确认的输入（不阻塞 P0–P4）

1. 新更新域名 / 对象存储桶 / GitHub 仓库 URL（P0 只能先落常量占位 + 旧域回退）
2. MM 插件是否需要签名与分发渠道（先按"哈希校验 + 用户确认"实现，签名可后置）
3. 混音台是否要送回（send/return）总线与母带链插槽数量（本计划按：每轨 8 槽 + 主通道 8 槽、无送返，预留字段）
4. 是否需要 MM 插件市场/社区分享（影响 P6 之后的分发设计）

## 九、里程碑汇总

| 里程碑 | 交付 | 前端交付（详见第四章） | 主要验收 |
|---|---|---|---|
| P-1 分支 | 主仓库 `feat/melodymate` + 引擎 `feat/melodymate-engine`（先清引擎未提交改动） | — | 两仓库分支就位、工作区干净 |
| P0 改名 | 全仓库（含引擎）更名 + 数据迁移 + 安装升级 | 4.9 品牌视觉刷新 + 文案替换 | 老版本升级数据无损、更新不断 |
| P1 声音管理 | 注册表后端 + 设置页页签 | 4.2 声音管理页签（分组卡/条目六态/AI 徽标） | SF2/采样包集中管理，全链路改走注册表 |
| P2 机架乐器箱 | arrangement v2 + ops API + 机架重写 + 卷帘音色 | 4.3 机架改造 + 音源选择器（三处共用） | 音色只能来自机架；AI 可操作轨道 |
| P3 素材库 | 三页签 + 搜索 + 显示优化 | 4.4 采样/音源/效果器页签 + 波形缩略 | 拖拽语义正确、双主题达标 |
| P4 混音台 | 通道条 + FX 基础设施 + 电平 | 4.5 混音台面板（升起动效/推子/电平表） | 1:1 通道、状态随工程持久化 |
| P5 VST3 | 扫描/乐器/效果/原生窗口/参数/AI 工具 | 4.6 参数面板 + 原生窗口伴随 UI | 真实插件全链路可用可存可恢复 |
| P6 MM 平台 | 规范 + SDK + 一键编译 + 运行时 + AI 闭环 | 4.7 插件 UI 承载窗 + 4.8 AI 界面 | 人类与 AI 各自能从零做出插件 |
| P7 收尾 | 回归 + 文档 + 发布 | 4.10 前端终检报告 | 审计级全绿 + 干净机器升级实测 |
