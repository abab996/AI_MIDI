# MelodyMate MM 插件开发指南（v1）

> 适用对象：想给 MelodyMate 写原生插件的人类开发者，以及在对话里替用户写插件的 AI。
> 配套阅读：[MM插件规范](MM插件规范.md)（包结构与 manifest 的权威 schema）、[引擎IPC协议](引擎IPC协议.md)（引擎方法字典）、[声音管理设计](声音管理设计.md)（装好之后在界面里长什么样）。
> 本文只写仓库里**真实存在**的行为。每条命令、字段、宏、消息都能在下列源码里找到出处：
> `engine/mm-sdk/include/mm/mm.h`、`engine/mm-sdk/examples/{gain,filter,synth}/`、`cmd/mm/main.go`、
> `internal/mm/{mm.go,install.go}`、`internal/soundlib/mm_install.go`、`internal/mcp/{mm_tools.go,approval.go}`、
> `frontend/mm-ui.html`、`frontend/src/settings.ts`、`engine/Source/Plugins/MmPluginHost.h`、
> `engine/Source/Audio/EngineAudioDevice.cpp`、`engine/Source/Main.cpp`、`internal/engine/supervisor.go`。

---

## 1. 你能做什么 / 前置条件

### 1.1 可做的四件事

| 想做的 | manifest 写法 | 跑在哪里 |
| :--- | :--- | :--- |
| 效果器 | `"kind": "effect"` | 轨道 FX 槽 / 主总线 FX 槽（0~7） |
| 乐器 | `"kind": "instrument"` | 轨道乐器（与 SF2、内置波形、VST3 乐器三选一） |
| 自绘界面 | 加 `"ui": { "entry": "ui/index.html", ... }` | 宿主承载窗里的 `sandbox` iframe |
| 暴露给 AI 的高层动作 | 加 `"commands": [...]` | **当前只有校验，没有消费方**（见 §1.4） |

DSP 走原生编译，与 VST3 共用同一条引擎音频链路；音频 I/O、线程、缓冲、参数平滑、参数记账全部由 SDK + 宿主托管，插件作者只写"处理逻辑 + 界面"。宿主侧的托管实现在 `engine/Source/Plugins/MmPluginHost.h`（`MmInstance`）。

### 1.2 本机需要什么

| 依赖 | 是否必需 | 说明与出处 |
| :--- | :--- | :--- |
| CMake ≥ 3.20 | **必需** | 脚手架的 `CMakeLists.txt` 写的是 `cmake_minimum_required(VERSION 3.20)`；`mm doctor` 用 `exec.LookPath("cmake")` 检测，缺失时给出的指引原文是「安装 CMake 3.20+：https://cmake.org/download/（勾选 Add to PATH）」 |
| MSVC 编译器 | 推荐 | `mm doctor` 检测 `cl` / `cl.exe`；**不在 PATH 也算通过**，因为 `cmake` 会用 vswhere 找到 VS 生成器并自行设置工具链（VS2022 Build Tools「使用 C++ 的桌面开发」就够）。doctor 只提示：「若 mm build 报找不到编译器，请从「x64 Native Tools Command Prompt」运行或安装 VS2022 Build Tools」 |
| MSBuild | 可选 | doctor 把它列为「MSBuild（可选）」，找不到时显示「未找到（cmake 会自行选择 VS 生成器；也可用 Ninja）」 |
| Go 工具链 | 构建 `mm.exe` 时需要 | `go build -o mm.exe ./cmd/mm`（[发布清单](发布清单.md) §0 要求 `mm.exe` 与主程序同目录） |
| MM SDK | `mm build` 时需要 | 单头文件 `engine/mm-sdk/include/mm/mm.h`，无第三方依赖（纯 C++17） |

`mm doctor` 的实际输出（本机实测，退出码 0）：

```
[mm] 检查构建工具链
  ⚠ cl.exe 不在 PATH：cmake 会自动定位 VS 生成器；若 mm build 报找不到编译器，请从「x64 Native Tools Command Prompt」运行或安装 VS2022 Build Tools（使用 C++ 的桌面开发）
  ✓ cmake            <cmake 路径>
  ✓ MSVC cl.exe      未在 PATH（cmake 走 VS 生成器）
  ✓ MSBuild（可选）      未找到（cmake 会自行选择 VS 生成器；也可用 Ninja）
[mm] 工具链就绪，可以 mm new / mm build
```

判据：只有 **cmake 缺失**才会让 `mm doctor` 返回非 0（`cl.exe` 与 MSBuild 两项恒为通过，见 `cmd/mm/main.go` 的 `cmdDoctor`）。

### 1.3 SDK 从哪里找（`mm build` 的解析顺序）

`mm build` 依次尝试，第一个找到的生效（`cmd/mm/main.go` 的 `cmdBuild` / `findSDKRoot`）：

1. 命令行 `--sdk <engine/mm-sdk>`；
2. 环境变量 `MM_SDK_ROOT`；
3. 从插件目录逐级向上最多 8 层找 `engine/mm-sdk/include/mm/mm.h`，再找 `mm-sdk/include/mm/mm.h`；
4. 从 `mm` 可执行文件所在目录同样向上找。

四步都找不到时报错原文：`找不到 MM SDK：用 --sdk <engine/mm-sdk> 指定，或设置 MM_SDK_ROOT 环境变量`。找到后会把路径作为 `-DMM_SDK_ROOT=<path>` 传给 CMake，并打印 `[mm] SDK: <path>`。

### 1.4 当前实现的边界（先知道这些，少走弯路）

以下几条是**规范书里写了、代码里还没落地**的部分。写到本文里是为了让你不要按规范书的写法去调不存在的 API：

| 边界 | 事实 | 出处 |
| :--- | :--- | :--- |
| `MM_CAP_*` 编译期宏 | SDK 头文件里**没有**任何 `MM_CAP_` 定义（全仓只有规范书与计划书提到）。插件里写 `#ifdef MM_CAP_FILES` 不会生效 | `engine/mm-sdk/include/mm/mm.h` 全文；`internal/mm/mm.go` 的 `knownCapabilities` |
| 参数表来源 | ABI 注释说"manifest 与运行期声明取并集，以 manifest 为准"，但引擎实际读的是 **ABI 的 `paramDecl`**（`MmInstance::paramCount` / `paramDecl` / `paramsToVar`）；manifest 的 `params` 目前只被校验器校验，界面与引擎的参数表都来自 ABI | `mm.h:105` 注释 vs `MmPluginHost.h` 的 `paramCount()/paramDecl()/paramsToVar()` |
| `commands[]` | 只做名字格式校验（`^[a-z][a-z0-9_]{2,63}$`），**没有任何代码**把它变成 AI 可调用的工具 | `internal/mm/mm.go` 的 `validateManifest`；MCP 工具表里没有对应实现 |
| 状态序列化 | 引擎 `plugin.getState` / `plugin.setState`（消息线程）→ `MmInstance::getState/setState`：把**宿主记账的参数现值**与插件自己的 DSP 状态打成一份信封 `[u8 ver=1][u8 kind=1][u16 paramCount][f32 values×n][u32 blobLen][blob]`（ABI 的 `getState` 只管 DSP 状态，参数权威在宿主）。Go 侧 `internal/plugins` 把 base64 负载写进 `<工程目录>/plugins/track_<n>_slot_<m>.state`（或 `master_slot_<m>.state`、`track_<n>_inst.state`；JSON 信封、单份上限 5MB），HTTP 入口是 `/api/engine/plugins/state/{save,load,list}`；引擎崩溃重启后由 `restorePluginStates` 推回最近一次状态 | `MmPluginHost.h` 的 `getState`/`setState`；`internal/plugins/plugins.go`；`internal/engine/supervisor.go` 的 `PluginGetState`/`restorePluginStates` |
| 参数持久化 | `arrangement.json` 只存槽位引用（`plugin` / `bypass` / `wet`）；插件参数现值与二进制状态存在上一条的 `<工程>/plugins/*.state` 里 | `frontend/src/arrangement/mixer.ts` 的 `commitFx`；`internal/plugins/plugins.go` |
| 走带信息 | 引擎给插件的是 `setTransport(120.0, 0.0, transport_.isPlaying())`——**BPM 恒为 120、拍位置恒为 0**，只有 `playing` 与 `sampleRate` 是真实值 | `EngineAudioDevice.cpp:1630` |
| `presets/` 目录 | 打包时会收进 `.mm`，但没有消费方（校验器不检查、界面不读） | `cmd/mm/main.go` 的 `packMM`；全仓无读取点 |
| 混音台装载菜单 | 混音台的「🔌 装载插件…」只列 VST3 清单（`GET /api/engine/plugins`）；MM 插件的装载入口是 `POST /api/engine/plugins/load`（`format:"mm"`） | `frontend/src/arrangement/mixer.ts` 的 `openPluginPicker`；`internal/engine/supervisor.go` 的 `LoadMmPlugin` |
| 机架分配 | 机架里的 `vst3`/`mm` 条目**不能**直接分配到轨道，界面会提示「插件音源请在混音台点空 FX 槽 →「装载插件」选择」 | `frontend/src/arrangement/arrange.ts` 的 `assignRackItem` |
| AI 工具的 MM 装载 | `mm_list` 的说明写「用返回的 id 走 `load_plugin`」，但 `load_plugin` 目前只调 `sup.LoadPlugin`（VST3 清单），没有 `format=mm` 分支；MM 的装载入口只有 HTTP `/api/engine/plugins/load` | `internal/mcp/arrange_tools.go` 的 `loadPluginText`、`mm_list` 的 description |
| 参数面板与状态随工程 | 编曲窗已接插件参数面板：轨道右键「🎛 插件参数面板…」→ `window.PluginPanel.open(...)`，工程打开时 `window.PluginPanel.restoreAll(...)` 把工程里记录的插件装回引擎并恢复状态；面板读写走 `/api/engine/plugins/params|param|state/*` | `frontend/src/arrangement/arrange.ts` 的 `openTrackMenu`；`frontend/src/arrangement/plugin_panel.ts` |

---

## 2. 五分钟第一个插件

### 2.1 生成脚手架

```bash
mm new <目录> --kind effect|instrument --name "显示名" --id com.example.myplugin
```

参数规则（`cmd/mm/main.go` 的 `cmdNew`）：

| 参数 | 必填 | 规则 |
| :--- | :--- | :--- |
| `<目录>` | 是 | 位置参数，**必须不存在**，否则报 `目录已存在: <dir>` |
| `--kind` | 否（默认 `effect`） | 只能是 `effect` 或 `instrument`，其它值报 `--kind 只能是 effect 或 instrument` |
| `--name` | 是 | 显示名；缺失时报 `必须给出 --id 与 --name（manifest 的必填字段）` |
| `--id` | 是 | 反向域名，正则 `^[a-z0-9][a-z0-9.-]{2,127}$` |

生成的目录结构（`mm new` 一次写出 5 个文件）：

```
<目录>/
├── CMakeLists.txt        # cmake_minimum_required 3.20、C++17、共享库、产物到 build/bin
├── manifest.json         # schema 1 / id / name / version 0.1.0 / kind / entry / api 1 / params / ui / author
├── README.md             # 提示 mm build / mm validate 与文档入口
├── src/plugin.cpp        # 一个可用的增益效果器骨架
└── ui/index.html         # 一个 range 滑杆 + postMessage 桥的最小界面
```

要点：

- `manifest.json` 的 `entry` 是 `win-x64/<id 里的点换成下划线>.dll`（例如 `com.example.myplugin` → `win-x64/com_example_myplugin.dll`）；
- `version` 从 `0.1.0` 起；
- `author.type` 默认 `human`——AI 生成的插件必须改成 `ai` 才会显示 ✨AI 徽标（见 §9）；
- 生成的 `ui/index.html` 已经演示了最小 UI 桥：发 `mm.ready` / `mm.setParam`，收 `mm.params` / `mm.theme`。

### 2.2 改两处代码

`src/plugin.cpp`（脚手架原文，改 `params()` 与 `process()` 即可）：

```cpp
#include <mm/mm.h>

class Plugin : public mm::Processor
{
public:
    Plugin() : gain_(0.0f, 1.0f, 0.8f) {}        // min / max / default

    void prepare(double sampleRate, int blockSize, mm::HostServices* services) override
    { sampleRate_ = sampleRate; blockSize_ = blockSize; (void)services; }

    void process(mm::AudioBufferView& audio, const mm::MidiView& midi, const mm::TransportInfo& transport) override
    {
        (void)midi; (void)transport;
        gain_.smooth(audio.numSamples, sampleRate_);   // 每块调用一次：约 10ms 时间常数
        const float g = gain_.value();
        for (int c = 0; c < audio.numChannels; ++c) {
            float* p = audio.channel(c);
            if (p == nullptr) continue;
            for (int i = 0; i < audio.numSamples; ++i) p[i] *= g;
        }
    }

    void onParam(int index, float normalized) override { if (index == 0) gain_.setNormalized(normalized); }

    const mm::ParamDecl* params(int& count) const override
    {
        static const mm::ParamDecl decls[] = { { "gain", "Gain", 0.0f, 1.0f, 0.8f, 0 } };
        count = 1;
        return decls;
    }

private:
    mm::SmoothedParam gain_;
    double sampleRate_ = 44100.0;
    int blockSize_ = 512;
};

MM_DECLARE_PLUGIN(Plugin, "com.example.myplugin", "我的插件", 0)   // 0 = effect，1 = instrument
```

`manifest.json`：把 `params` 里的 `id` / `min` / `max` / `default` / `curve` 与 `params()` 里的 `ParamDecl` 保持一致——引擎与界面用的是 `ParamDecl`（ABI），manifest 的这份声明供校验器核对。

### 2.3 `mm build`

```bash
mm build [目录] [--out <file.mm>] [--config Release] [--sdk <mm-sdk>]
```

七个阶段，逐个可验证（`cmd/mm/main.go` 的 `cmdBuild`）：

| 阶段 | 行为 | 失败时的原话 |
| :--- | :--- | :--- |
| 读 manifest | 读 `<目录>/manifest.json`，要求 `id`/`version`/`entry` 非空 | `读取 manifest.json 失败（是否在插件目录里？）` / `manifest 缺 id/version/entry（先补齐再 build）` |
| 找 SDK | §1.3 的顺序，打印 `[mm] SDK: <path>` | `找不到 MM SDK：用 --sdk <engine/mm-sdk> 指定，或设置 MM_SDK_ROOT 环境变量` |
| 配置 | `cmake -S . -B build -DCMAKE_BUILD_TYPE=<config> -DMM_SDK_ROOT=<sdk>` | `cmake ... 失败: <err>` |
| 编译 | `cmake --build build --config <config>` | 同上 |
| 收集 | 到 `bin/<platform>/`（Windows x64 = `win-x64`）按 `entry` 的库名找产物 | `编译产物未找到（期望 <lib><ext>）——检查 CMakeLists 的 OUTPUT_NAME 与 RUNTIME_OUTPUT_DIRECTORY` |
| 写哈希 | 计算 `bin/<platform>/<lib>` 的 sha256，写回 `manifest.json` 的 `hash.files` | — |
| 打包 + 校验 | 打 `.mm`（默认 `<id>-<version>.mm`，`--out` 可改），随后用宿主同源校验器复核 | `打包后校验失败（包已生成，但不可安装）: <问题清单>` |

打包只收这些路径（`packMM` 的白名单）：`manifest.json`、`bin/`、`ui/`、`presets/`、`LICENSE`、`README.md`；`build/` 与 `*.mm` 自动排除。成功时打印 `[mm] 已生成 <out>（<KB> KB，校验通过）`，含 UI 时再打印 `[mm] 含 UI: <entry>`。

### 2.4 `mm validate` / `mm inspect`

```bash
mm validate <pkg.mm>      # 校验通过：<name> <version>（<kind>，N 个文件）
mm inspect <pkg.mm>       # 打印 id/name/version/kind/entry/api/vendor/capabilities/ui/params + 文件清单（不安装）
```

实测（`mm inspect` 一个官方示例）：

```
id        : com.melodymate.example.filter
name      : MM Example Filter
version   : 1.0.0
kind      : effect
entry     : win-x64/com_melodymate_example_filter.dll
api       : 1
vendor    : MelodyMate
capabilities: （无：纯 DSP，最小权限）
ui        : ui/index.html (360x180)
params    : 3
  - cutoff       20..18000 default=1200
  - resonance    0.5..8 default=0.9
  - mix          0..1 default=1

文件（4）:
  README.md
  bin/win-x64/com_melodymate_example_filter.dll
  manifest.json
  ui/index.html
```

若包标记了 `author.type = "ai"`，`validate` 与 `inspect` 都会额外打印一行 AI 生成提示。

### 2.5 `mm install` 与 `--grant` 的真实语义

```bash
mm install <pkg.mm> --dest <数据根>/Library/plugins [--grant files,network]
```

安装规则（`cmd/mm/main.go` 的 `cmdInstall` 与 `internal/mm/install.go`，命令行与界面走**同一套**门禁）：

1. 先 `validate`，不通过直接拒绝安装；
2. **声明而未授权 → 拒绝**：`插件声明了能力 "network" 但未授权：安装命令需显式 --grant network（用户须在界面上确认能力清单）`；
3. **授权了未声明的能力 → 拒绝**（不允许多授权）：`--grant 包含插件未声明的能力 "threads"（不允许多授权）`；
4. 目标目录 `<dest>/<id>@<version>/` 已存在 → `目标已存在: <dir>（先卸载或换版本号）`；
5. 解包时逐条校验路径，命中穿越直接以 `MM008` 拒绝；解包失败会删掉半成品目录。

`--grant` 是"**用户已确认**的能力清单"的传递通道，不是插件自己的声明。界面上对应两步式安装：先"预览能力"（只校验不落盘），勾选后再带 `grant` 安装（见 §9 与 [声音管理设计](声音管理设计.md) §4）。纯 DSP 插件（`capabilities: []`）无需任何授权即可安装。

### 2.6 在客户端里看到它、装载它、听到它、打开它的界面

| 步骤 | 操作 | 背后发生的事 |
| :--- | :--- | :--- |
| 1. 入册 | 设置 → 声音管理 → 「↻ 同步已安装插件」 | `POST /api/engine/plugins/sync` → `soundlib.SyncAllInstalledMM()` 扫描 `Library/plugins/` 下每个目录，读 `manifest.json` 写入注册表（`kind=mm`、`category=<kind>`、`classUid=<插件 id>`、`capabilities`、`hash`、`origin`） |
| 2. 或者用界面安装 | 声音管理顶部「安装 .mm 插件」输入包路径 → 「预览能力」 → 勾选 → 「安装」 | `POST /api/engine/plugins/install`：`preview:true` 返回 `{preview, needsGrant}`；正式安装带 `grant` 数组，成功后自动同步注册表并返回 `{ok, installed, added, updated, failed}`。包路径必须落在 `Library/plugin-src/` 或 `Library/plugins/` 内（否则 403 `只允许安装 Library/plugin-src 下的 .mm 包`） |
| 3. 装载到引擎 | `POST /api/engine/plugins/load`，body 形如 `{"format":"mm","path":"<Library/plugins/<id>@<ver>/bin/win-x64/x.dll>","track":0}`；效果器再加 `"slot":0`；主总线用 `"master":true` | `internal/engine/supervisor.go` 的 `LoadMmPlugin` 先做路径校验（必须位于已安装插件目录内，fail-closed），再让引擎走 `plugin.load` 的 `format=mm` 分支：`dlopen` → `mm_get_factory` → ABI 比对 → `create()` → `prepare()` |
| 4. 发声 | 乐器：弹奏该轨（MIDI 经无锁环投递给插件）；效果器：音频经 FX 槽 | MM 乐器在轨道渲染里**优先级最高**（与 VST3 乐器、SF2 三选一）；效果器在 FX 链中按块长分片调用 |
| 5. 打开界面 | MM 条目上的 🎛，或混音台 FX 槽菜单的「🎛 插件界面（MM 承载窗）…」 | 打开 `/mm-ui.html?plugin=<插件 id>&track=<n>`（或 `&master=1&slot=<n>`），承载页再用一次性票据加载 `/api/engine/plugins/ui?...` 里的插件页面 |

装载接口的两种"装错"会被明确拒绝（引擎原文）：

- 拿效果器当轨道乐器：`这是效果器插件（kind=effect）：请装到 FX 槽（带 slot 参数），或用 loadMmInstrument 只装乐器`；
- 拿乐器往 FX 槽塞：`这是乐器插件（kind=instrument）：MM 乐器挂轨道的能力在下一批落地，请用效果器或先用 VST3 乐器`——**这句文案与实现不一致**：`loadMmInstrument` 已经可用（`engine_smoke -mmsynth` 就是用它量的电平），实际限制只是"FX 槽不接受乐器"。以代码路径为准。

### 2.7 `mm test` 现在只做什么

```bash
mm test [目录]
```

它检查 `<目录>/bin/<platform>/<entry 的库名>` 是否存在，然后打印：

```
[mm] 库已就绪: <libPath>
[mm] 渲染自测由宿主执行：把包安装后在引擎里加载（插件崩溃不会影响主程序）
[mm] 提示：mm build 已包含 mm validate 的等价校验，可直接安装
```

也就是说：**离线渲染自测尚未在这个命令里实现**（Go 里跨平台动态加载需要 cgo/系统 API）。想真正量到"插件出声"，用 §11 里的 `engine_smoke -mmgain/-mmfilter/-mmsynth`。

---

## 3. manifest.json 字段逐个说明

schema 的权威定义在 `internal/mm/mm.go` 的 `Manifest` 结构体，校验规则在同文件的 `validateManifest`；[MM插件规范](MM插件规范.md) §2 是同一份契约的文字版。

### 3.1 顶层字段

| 字段 | 类型 | 必填 | 取值与校验 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| `schema` | int | 是 | `> 0` 且 `≤ 1`（宿主 `SchemaVersion = 1`） | 缺 → `MM003`；高于宿主 → `MM004` |
| `id` | string | 是 | `^[a-z0-9][a-z0-9.-]{2,127}$` | 反向域名。安装目录名与注册表 `classUid` 都用它 |
| `name` | string | 是 | 非空白，`≤ 64` 个字符（按 rune 计） | 显示名 |
| `version` | string | 是 | `^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$` | semver；同 id 多版本可共存（`<id>@<ver>`） |
| `vendor` | string | 否 | — | 供应商，界面「厂商」行 |
| `kind` | string | 是 | 只能是 `effect` 或 `instrument` | 决定装槽还是装轨 |
| `entry` | string | 是 | 相对 `bin/` 的库路径，必须**存在于包内**且不是目录；不得含 `..`、不得以 `/` 开头、不得带盘符 | 例：`win-x64/myplugin.dll` |
| `api` | int | 是 | 必须**精确等于** `1`（`MM_ABI_VERSION`） | 不等 → `MM005`；原生库与宿主同进程，ABI 不同即崩溃 |
| `capabilities` | string[] | 否 | 只能取 `files` / `network` / `threads` / `midi_out`；不得重复 | 未知项 → `MM009`（fail-closed）；空数组 = 纯 DSP |
| `params` | 对象数组 | 否 | 见 §3.2 | 只被校验器校验格式与取值；**引擎与界面的参数表都来自 ABI**（§1.4） |
| `ui` | 对象 | 否 | 见 §3.3 | 不写 = 无自绘界面，宿主承载窗退化为参数面板 |
| `commands` | 对象数组 | 否 | 见 §3.4 | 当前只校验、不消费 |
| `author` | 对象 | 否 | `{"type": "human"}` 或 `{"type": "ai"}` | `ai` → 声音管理显示 ✨AI 徽标 |
| `hash` | 对象 | 否 | 见 §3.5 | `mm build` 自动写入 |

未知的顶层字段会被 `json.Unmarshal` 忽略（前向兼容）。

### 3.2 `params[]`

| 字段 | 必填 | 校验 |
| :--- | :--- | :--- |
| `id` | 是 | 非空白、全表唯一（重复 → `MM003`） |
| `name` | 否 | 界面显示名 |
| `min` / `max` | 是 | `max > min`，否则 `MM003` |
| `default` | 否 | 必须落在 `[min, max]` 内 |
| `unit` | 否 | 单位文本（`Hz`、`Q`、`%`…） |
| `curve` | 否 | `""` / `linear` / `log` / `exp`，其它值 → `MM003` |
| `steps` | 否 | `≥ 0`；`0` = 连续，`> 0` = 离散档位 |
| `automatable` | 否 | 布尔（指针类型，缺省即未声明） |

### 3.3 `ui`

| 字段 | 校验 |
| :--- | :--- |
| `entry` | 非空、存在于包内、不得含 `..`/绝对路径/盘符；否则 `MM012`（路径非法时给 `MM008`） |
| `width` / `height` | `0 ~ 4096`，越界 → `MM012`；`0` 表示未声明，承载窗用默认尺寸 |
| `resizable` | 布尔，承载窗可缩放 |

承载窗打开时用 `ui.width/height` 作为初始窗口尺寸：`GET /api/engine/plugins/installed` 返回的 `uiWidth`/`uiHeight` 由前端预取缓存（`frontend/src/arrangement/mixer.ts` 的 `prefetchMMSizes`），拿不到就用 `760×560`。

### 3.4 `commands[]`

| 字段 | 校验 |
| :--- | :--- |
| `name` | `^[a-z][a-z0-9_]{2,63}$`（能当工具名用），否则 `MM003` |
| `description` | 可选文本 |
| `params` | 可选，原样保留的 JSON（`json.RawMessage`） |

### 3.5 `hash`

| 字段 | 校验 |
| :--- | :--- |
| `algorithm` | 只能是 `sha256`（留空视为 sha256），其它值 → `MM010` |
| `files` | 键是包内相对路径（`bin/win-x64/x.dll`），值是 64 位 hex；键不在包内 → `MM010`；值格式不对 → `MM010`；内容不匹配 → `MM010` |

`mm build` 每次都会重算并写回；`internal/mm.InstallPreview.HashVerified` 的规则是"有 `hash` 且 `files` 非空"，安装界面据此显示「哈希已校验 / 未声明哈希」。

---

## 4. DSP 与 ABI

### 4.1 单一 C 符号 + 函数指针表

宿主与插件同进程，通过**唯一的导出符号** `mm_get_factory` 交换 `mm::PluginVTable`（避免 C++ 名字修饰与 STL 跨边界问题）。`MM_DECLARE_PLUGIN` 负责生成这个符号：

```cpp
MM_DECLARE_PLUGIN(ProcessorClass, pluginId, pluginName, pluginKind)   // pluginKind: 0=effect, 1=instrument
```

宏展开会做四件事：定义 `info()`（返回 id/name/kind）、`create()`/`destroy()`、把每个虚函数包成 C 函数指针，以及**导出一个 `mm_get_factory` 返回 `PluginVTable`**。注意宏有 **4 个参数**——规范书 §5 里的 `MM_DECLARE_PLUGIN(MyProcessor)` 单参数写法在 SDK 里不存在。

### 4.2 数据结构

| 结构 | 字段 | 语义 |
| :--- | :--- | :--- |
| `mm::AudioBufferView` | `float* const* channels`、`numChannels`、`numSamples` | 非交错；`channels[c]` 长度为 `numSamples`。`clear()` 清零全部声道，`channel(c)` 取某声道指针（越界返回 `nullptr`） |
| `mm::MidiView` | `const uint8_t* data`、`size` | **3 字节消息序列**（status/data1/data2）。`count() = size/3`，`at(i,status,d1,d2)` 取第 i 条 |
| `mm::TransportInfo` | `bpm`、`ppqPosition`、`playing`、`sampleRate` | 只读快照。`ppqPosition` 1.0 = 一拍。当前 `bpm`/`ppqPosition` 是占位值（§1.4） |
| `mm::HostServices` | `hostContext`、`setParam(hostContext,index,normalized)`、`getParam(...)`、`log(hostContext,msg)` | 宿主回调。`setParam` 写参数（宿主负责平滑与 UI/AI 同步）；`log` 委托给 `juce::Logger`，**不要在音频线程调用**；`getParam` 当前返回 `0.0f` 占位（宿主权威值由插件自己的 `SmoothedParam` 持有） |
| `mm::ParamDecl` | `id`、`name`、`minValue`、`maxValue`、`defaultValue`、`curve` | `curve`：`0 = linear`、`1 = log`、`2 = exp` |
| `mm::PluginInfo` | `id`、`name`、`kind` | `kind`：`0 = effect`、`1 = instrument` |
| `mm::PluginVTable` | `abiVersion`、`info`、`create`、`destroy`、`prepare`、`process`、`reset`、`paramCount`、`setParam`、`paramDecl`、`getState`、`setState` | 宿主只调这些 |

### 4.3 你要实现的虚函数（`mm::Processor`）

| 虚函数 | 线程 | 约定 |
| :--- | :--- | :--- |
| `prepare(double sampleRate, int blockSize, HostServices* services)` | 消息线程 | 采样率/块长变化时调用，**可以分配内存**。脚手架里存下 `sampleRate`/`blockSize` 备用 |
| `process(AudioBufferView&, const MidiView&, const TransportInfo&)` | 音频线程 | **纯虚**，必须实现。零分配、零锁、不阻塞（§4.6） |
| `reset()` | 音频线程 | 停止/定位时清内部状态（示例 synth 在这里复位相位与按键门） |
| `onParam(int index, float normalized)` | 音频线程 | 宿主写完参数后调用，插件只做赋值（典型是 `param_.setNormalized(v)`） |
| `params(int& count) const` | 消息线程 | 返回静态 `ParamDecl` 数组并写出数量；默认返回 `nullptr, 0` |
| `getState(uint8_t*& data, int& size)` | 消息线程 | 可选；返回 `false` 表示不实现（此时信封里仍有参数段）。宿主经 `plugin.getState` 调用，结果写进工程的状态文件 |
| `setState(const uint8_t* data, int size)` | 消息线程 | 可选；宿主先按信封的参数段写回参数，再调它恢复 DSP 状态。数据格式不认识时宿主拒绝整份状态 |

### 4.4 参数与平滑（`mm::SmoothedParam`）

```cpp
SmoothedParam(float minValue, float maxValue, float defaultValue, int curve = 0);
void  setNormalized(float v);        // 宿主调用（可在音频线程）：clamp 到 0~1 后写入 target
void  smooth(int numSamples, double sampleRate);   // 每个块调一次
float normalized() const;            // 当前平滑值（0~1）
float value() const;                 // 当前平滑值的实际单位值
float targetValue() const;           // 目标值的实际单位值
```

要点：

- 平滑时间常数约 **10ms**（`coeff = 1 - exp(-numSamples / (sampleRate * 0.01))`），差值小于 `1e-6` 时直接吸附到目标，避免残差；
- 曲线换算与宿主一致：`log` 用 `min * pow(max/min, t)`，`exp` 用 `min + (max-min)*t²`，`linear` 用线性插值。宿主侧还有一份等价实现（`MmInstance::normalizeValue`），因为初始值要按同一条曲线从 `defaultValue` 反算归一化值；
- 宿主把参数现值记在自己的数组里（`kMaxParams = 64`，超出索引不记账），这是"UI 回读"的权威来源——ABI 有意不提供"读当前值"；这份记账值同时是状态信封里的参数段（§1.4）。

### 4.5 采样率、块长与缓冲约定

| 约定 | 值 | 出处 |
| :--- | :--- | :--- |
| 采样率 | 音频设备实际采样率，`prepare` 时告知 | `EngineAudioDevice::loadMmInstrument` 传 `sampleRate_` |
| 块长 | `prepare` 的 `blockSize` 是设备缓冲（`maxBlockSize`，默认 2048，`jmax(64, 设备缓冲)`）；`process` 传入的 `numSamples` **不会超过它**（宿主会 clamp） | `MmPluginHost.h` 的 `process`；`EngineAudioDevice.h` 的 `maxBlockSize` |
| 效果器分片 | FX 链传入的是整块缓冲（长度可以大于设备块长），宿主按 `blockSizeSafe()`（`jmax(64, blockSize)`）**分片**循环调用你的 `process` | `MmFxProcessor::process` |
| 声道数 | 轨道渲染给插件的是 **2 声道**视图：单声道缓冲会把右声道指针指向左声道（两声道内容相同）；你自己按 `numChannels` 循环即可 | `MmInstance::process` 的 `numCh == 1` 分支 |
| MIDI 容量 | 每块最多 **512 条** 3 字节消息（宿主缓冲 `3 * 512` 字节），超出部分被丢弃 | `midiBuf_[3 * 512]` |
| MIDI 事件 | 宿主把 `noteOn` → `0x90|ch`、`noteOff` → `0x80|ch, vel=0`、`allNotesOff` → `0xB0|ch, d2=123` 写进环再交给插件 | `MmInstance::process` 的 `ev.kind` 分支 |
| 轨道/槽位上限 | 32 轨（`kMaxTracks`）、每轨与主总线各 8 个 FX 槽（`kMaxFxSlots`） | `MixerGraph.h`、`FxChain.h` |

### 4.6 实时线程纪律（`process` 里不能做的事）

| 禁止 | 原因 | 替代做法 |
| :--- | :--- | :--- |
| `new` / `malloc` / 容器扩容 | 音频线程零分配是宿主保证的前提 | 在 `prepare` 里预分配，或改用静态数组（示例 filter 用 `float ic1_[8]`） |
| 互斥锁 / 条件变量 / 阻塞等待 | 会与音频回调互相卡死 | 原子变量；参数走 `SmoothedParam`（内部 `std::atomic`） |
| 文件 / 网络 / 数据库 | 无界延迟 | 需要时声明对应能力并放到自己的线程（能力和线程当前尚未在 SDK 里开放，见 §1.4） |
| 日志 / `printf` / `HostServices::log` | 头文件明确写「不要在音频线程调用」 | 只在 `prepare` 或错误路径里写；`log` 会走 `juce::Logger` |
| 让异常越过边界 | 宿主不捕获插件异常 | 内部 try/catch 或返回安全值 |
| 依赖 `TransportInfo.bpm/ppq` 做时间同步 | 当前是占位值（120/0） | 用 `sampleRate` 自己数样本 |

`process` 里可以放心做的事：读写 `AudioBufferView`、遍历 `MidiView`、读写 `SmoothedParam`、做纯计算、读 `TransportInfo.playing/sampleRate`。

---

## 5. 能力模型：默认最小权限

### 5.1 四个能力取值

`internal/mm/mm.go` 的 `knownCapabilities` 是唯一的白名单：

| capability | 界面上的说明文案（`frontend/src/settings.ts` 的 `CAP_LABELS`） |
| :--- | :--- |
| `files` | `files — 读写自己的插件目录（Library/plugins/<id>@<ver>/data/）` |
| `network` | `network — 允许出网（默认拒绝）` |
| `threads` | `threads — 允许自建线程（默认只用宿主音频线程）` |
| `midi_out` | `midi_out — 允许产生 MIDI 输出（琶音器/和弦器类）` |

`capabilities` 为空（或不写）就是"纯 DSP"：只能读 MIDI/音频、写参数、读走带信息。安装界面会明确写「该插件不申请任何特权能力（纯 DSP），可直接安装。」

### 5.2 真实的强制点

目前生效的是**校验期 + 安装期 + 工具期**三层，而不是编译期宏（§1.4）：

| 层 | 规则 | 出处 |
| :--- | :--- | :--- |
| 校验器 | 未知能力 → `MM009`（fail-closed）；重复声明 → 也是 `MM009` | `internal/mm/mm.go` 的 `validateManifest` |
| 安装门禁 | 声明了但未授权 → 返回 `MissingGrant` 清单并**不落盘**；授权了未声明的能力 → 直接报错拒绝 | `internal/mm/install.go` 的 `PreviewInstall` / `Install` |
| 命令行 | 同上两条规则在 `mm install` 里各有一份等价实现 | `cmd/mm/main.go` 的 `cmdInstall` |
| HTTP | `POST /api/engine/plugins/install`：`preview:true` 只返回预览（含 `missingGrant`），正式安装带 `grant` 数组；失败走 400 并回传原文 | `internal/server/handler_plugins.go` 的 `/install` |
| AI 工具 | `mm_install_plugin` 必须带用户批准的 `approval_id`（§10），`grant` 只能是插件声明的子集 | `internal/mcp/mm_tools.go` |
| 引擎装载 | 原生库路径必须位于 `Library/plugins/` 内（先词法判定，再解析符号链接复核）；未配置插件目录一律拒绝 | `internal/engine/supervisor.go` 的 `validateMmPluginPath` |
| 注册表 | 装好后把 `capabilities` 记进条目，详情弹窗「能力」一行展示 | `internal/soundlib/mm_install.go` 的 `SyncInstalledMM` |

### 5.3 被拒绝时看到什么

| 场景 | 文案（原文） |
| :--- | :--- |
| 声明未授权（安装器） | `插件声明了能力 [network] 但尚未授权：请在界面确认能力清单后用 --grant/network 安装` |
| 声明未授权（命令行） | `插件声明了能力 "network" 但未授权：安装命令需显式 --grant network（用户须在界面上确认能力清单）` |
| 多授权 | `授权了插件未声明的能力 "threads"（不允许多授权）` |
| 未知能力 | `MM009: 未知能力（fail-closed 拒绝）: kernel_driver` |

---

## 6. UI 桥：`ui/index.html` 怎么被宿主加载

### 6.1 三级结构

```
宿主页面（编曲窗 / 混音台 / 设置页）
   │  window.open("/mm-ui.html?plugin=<id>&track=<n>")  ← 承载窗，同源
   ▼
frontend/mm-ui.html（承载页：协议翻译 + 校验 + 参数面板 + 骨架屏）
   │  <iframe sandbox="allow-scripts">（无 allow-same-origin ⇒ 不透明源）
   ▼
插件自绘界面（Library/plugins/<id>@<ver>/ui/index.html）
```

中间必须有承载页：插件 UI 来自本地安装目录，必须跑在**无 same-origin 的沙箱**里；承载页做三件事——把插件的 `mm.*` 消息翻译成宿主 HTTP 调用、把宿主推来的参数/主题/走带转发给插件、丢弃字段类型非法的消息（`dropped++` 计数，插件界面崩坏不影响宿主）。

承载页 query：`?plugin=<插件 id>&track=<n>`、`?plugin=<id>&slot=<n>`（效果器）、主总线再 `&master=1`，可选 `&theme=dark|parchment`。缺 `plugin` 时页面直接给出用法提示并不再加载 iframe。

### 6.2 消息表（逐条对应 `frontend/mm-ui.html`）

| 方向 | 消息 | 字段 | 说明 |
| :--- | :--- | :--- | :--- |
| 插件 → 承载页 | `mm.ready` | — | 页面就绪：承载页隐藏骨架屏、显示「插件已就绪」，随后 `GET /api/engine/plugins/params` 拉全量参数 |
| 承载页 → 插件 | `mm.params` | `params: [{id, index, value, text}]` | 全量参数。`value` 是**归一化 0~1**；`text` 当前就是 `String(value)` |
| 插件 → 承载页 | `mm.setParam` | `{id, value}` | 写参数：先按 `id` 查索引（未知 id → `dropped++` 并提示「未知参数 <id>」），clamp 到 0~1 后 `POST /api/engine/plugins/param` |
| 承载页 → 插件 | `mm.paramChanged` | `{id, value}` | 承载页**底部参数面板**改动时通知插件同步界面（宿主工具条 → 插件） |
| 插件 → 承载页 | `mm.resize` | `{width, height}` | 尺寸上报：`0 < w,h ≤ 4096` 时把 iframe 高度设为 `max(120, height)`（宽度不跟随） |
| 插件 → 承载页 | `mm.tooltip` | `{text}` | 悬浮/聚焦提示：承载页写进顶栏右侧状态位 |
| 宿主 → 承载页 → 插件 | `mm.theme` | `{theme: "dark"\|"parchment"}` | 双主题令牌。承载页同时把它设到自己的 `data-theme` 再转发给插件 |
| 宿主 → 承载页 → 插件 | `mm.transport` | `{bpm, playing, beat}` | 走带。**承载页自己不轮询 HTTP**：宿主窗口每秒推一次（混音台 `pushTransport`），承载页缓存到 `window.__mmTransport` 并转发；宿主没接时插件收不到，必须自行降级 |

其余类型（或以 `mm.` 开头但字段类型不对的消息）一律记为丢弃。

### 6.3 加载顺序（实测路径）

```
1. 承载页 POST /api/engine/plugins/ui-ticket            → {ticket, expires_in: 60}
2. 承载页设置 iframe.src = /api/engine/plugins/ui?plugin=<id>&ticket=<ticket>
      · iframe 是浏览器导航，带不了 X-MM-Token，所以用一次性票据（60 秒有效、用完即废）
      · 票据只能被消费一次：承载页不会对同一 URL 再做探测请求
3. 承载页 GET /api/engine/plugins/params?track=…        → 参数表（建 id↔index 映射、渲染底部面板）
4. 承载页 post({type:"mm.params", params:[…]})           → 插件据此初始化控件
5. 承载页 post({type:"mm.theme", theme})                 → 插件据此取色
6. 插件 post({type:"mm.ready"})                          → 骨架屏消失；承载页再拉一次参数（保证顺序安全）
7. 之后：插件 mm.setParam ⇄ 承载页 mm.paramChanged；宿主按需推 mm.transport / mm.theme
```

### 6.4 回退与安全

- **无 UI 的插件**：`ui` 字段不写时，iframe 会拿到 404（`插件 UI 资源不存在：ui/index.html`），承载页顶部状态位显示「插件界面加载失败…」，但**底部「参数」按钮照常可用**——这就是无界面插件的调参面板（没有参数时显示「该插件没有可调参数」）；顶栏还有「重载」按钮重新走一遍 `loadFrame`。
- **参数接口的取值语义**：`GET /api/engine/plugins/params` 返回的 `value` 是**归一化值**（MM 走 `MmInstance::paramsToVar`，`value = 宿主记账的 0~1`；VST3 走 `p->getValue()`），而承载页参数面板按 `(value - min) / (max - min)` 反算滑杆位置。MM 的 `min`/`max` 是真实量程（例如 0~2），所以对非 0~1 量程的参数，滑杆位置与显示值不会与插件界面里的实际值一致；写入方向不受影响（面板发的就是归一化值，宿主再 clamp 到 0~1）。
- **postMessage 的目标源**：承载页对插件 iframe 一律用 `"*"`（iframe 是不透明源，指定具体 origin 会让消息被静默丢弃）；插件自己的代码已在沙箱内，无法借这条通道提权。
- **来源校验**：承载页只接受 `ev.source === window.parent` 或 `window.opener` 的宿主消息；插件的消息按 `ev.source` 核对（必须是那个 iframe 的 `contentWindow`）——`ev.origin` 对不透明源是空串，不能当判据。
- **静态资源白名单**：`/api/engine/plugins/ui` 只服务 `.html/.js/.css/.json/.svg/.png/.jpg/.jpeg/.gif/.webp/.woff/.woff2/.ttf`，其余 403；路径必须落在该插件的安装目录内。
- **响应头**：`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。

---

## 7. 三个官方示例精讲

三个示例都在 `engine/mm-sdk/examples/` 下，且都是 `mm build` 的回归用例（仓里已带打包好的 `.mm`）。

### 7.1 `gain` —— 最小效果器（人类教程第一步）

- **演示特性**：单参数、`SmoothedParam` 平滑、只写 DSP。
- **参数**：`{"id":"gain","name":"Gain","min":0,"max":2,"default":1,"curve":"linear"}`；`SmoothedParam gain_(0.0f, 2.0f, 1.0f)`；`ParamDecl { "gain", "Gain", 0.0f, 2.0f, 1.0f, 0 }`。
- **`process`**：`gain_.smooth(numSamples, sampleRate_)` → 取 `value()` → 逐声道逐样本相乘；`midi`/`transport` 显式 `(void)` 掉。
- **`kind`**：`MM_DECLARE_PLUGIN(GainProcessor, "com.melodymate.example.gain", "MM Example Gain", 0)` —— 第 4 个参数 `0` = 效果器。
- **界面**（`ui/index.html`）：一个 range 滑杆，`input` 时发 `mm.setParam{id:"gain", value}` 并把 0~1 归一化值乘 2 显示为实际增益；收 `mm.params` 回填、收 `mm.theme` 切主题；末尾 `send("mm.ready")`。
- **CMakeLists**：`get_filename_component(MM_SDK_ROOT .../../..)` 回落 + `-Wall`（非 MSVC）/ `/utf-8 /W3`（MSVC），产物 `PREFIX ""`、输出到 `${CMAKE_BINARY_DIR}/bin`。

### 7.2 `filter` —— 多参数、曲线与每声道状态

- **演示特性**：三参数 + 对数曲线；滤波器有记忆，必须**每声道独立状态**；数值兜底。
- **参数**：`cutoff`（20~18000，`curve=1` 对数）、`resonance`（0.5~8，线性）、`mix`（0~1，线性）。构造时三个 `SmoothedParam` 的曲线参数与 `ParamDecl` 的最后一位一一对应。
- **DSP**：TPT/零延迟反馈 SVF（`g = tan(π·fc/fs)`、`k = 1/Q`、`a1 = 1/(1+g(g+k))`、`a2 = g·a1`、`a3 = g·a2`）。源码注释记录了一条真踩过的坑：更简单的 Chamberlin 形式在高截止频率下发散（18kHz 直接静音），所以用 TPT；并且仍要把 `fc` 钳在 `0.45·fs` 以下、下限 10Hz。
- **每声道状态**：`float ic1_[kMaxChannels]; float ic2_[kMaxChannels];`（`kMaxChannels = 8`），循环内把该声道状态取到局部变量、算完写回；声道数取 `min(audio.numChannels, kMaxChannels)`。
- **数值兜底**：写回后 `if (!std::isfinite(ic1_[c]) || !std::isfinite(ic2_[c]))` 就清零，避免 NaN 永久污染输出。
- **`prepare`**：把 `sampleRate <= 0` 兜成 44100，并清零滤波器状态。
- **界面**：自带 `DEFS` 表（min/max/curve/unit/digits）做"归一化 → 实际值"换算，与 ABI 的 `ParamDecl` 对齐；`input` 时发 `mm.setParam`；**focus 时发 `mm.tooltip`** 把实际值报给宿主状态栏；脚本末尾先 `send("mm.resize", {width:360, height:180})` 再 `send("mm.ready")`；`mm.theme` 只接受 `dark`/`parchment` 两个值。

### 7.3 `synth` —— 乐器与 MIDI

- **演示特性**：`kind=1` 的乐器写法、读 MIDI 维护复音门、`reset()`。
- **MIDI 处理**：遍历 `midi.count()`，`midi.at(i, st, d1, d2)`；`(st & 0xf0) == 0x90 && d2 > 0` 记 `gate_[note] = d2/127`；`0x80` 或 `0x90` 且 `d2 == 0` 关掉该音；`(st & 0xf0) == 0xb0 && note == 123`（All Notes Off）清空 128 个门。
- **发声**：固定 440Hz（A4）正弦——源码注释明确这是示例简化（真实插件应按 note 换算频率）；每个样本把 128 个门求和（上限 1.0）乘增益。
- **`reset()`**：复位相位与全部门（宿主在停止/定位时调用）。
- **为什么它在仓里重要**：它是"无许可依赖的出声回归资产"——VST3 乐器受第三方授权限制（未授权插件渲染静音），MM 乐器由本仓 SDK 编译，`engine_smoke -mmsynth` 必须量到非零轨道电平（实测峰值 `0.4601`，见 [发布清单](发布清单.md) §5）。
- **`prepare`**：清零相位与 `gate_[128]`。

### 7.4 三个示例覆盖的特性对照

| 特性 | gain | filter | synth |
| :--- | :---: | :---: | :---: |
| 效果器路径（FX 槽） | ✅ | ✅ | — |
| 乐器路径（轨道 + MIDI） | — | — | ✅ |
| 多参数 + 对数曲线 | — | ✅ | — |
| 每声道内部状态 | — | ✅ | —（相位是全局单声道振荡） |
| 数值兜底（NaN/Inf） | — | ✅ | — |
| `reset()` | — | — | ✅ |
| 自绘界面 | ✅（最简） | ✅（含 tooltip / resize） | ✅ |
| `mm.tooltip` / `mm.resize` | — | ✅ | — |

---

## 8. 调试与排错

### 8.1 构建失败

| 现象 | 原因与处理 |
| :--- | :--- |
| `找不到 MM SDK` | 用 `--sdk <engine/mm-sdk>`，或设 `MM_SDK_ROOT`，或把插件工程放在仓库树内（§1.3 的向上查找） |
| `mm/mm.h: No such file or directory` | 同上——`mm build` 会先报 SDK 缺失而不是把编译器错误丢给你 |
| `manifest 缺 id/version/entry` | 先补齐 `manifest.json` 再 build |
| `编译产物未找到（期望 xxx.dll）` | 检查 `CMakeLists.txt` 的 `OUTPUT_NAME` 与 `RUNTIME_OUTPUT_DIRECTORY`；`mm build` 会在 `build/bin/`、`build/bin/<config>/` 下找同名库，最后兜底遍历 `build/` |
| `cl.exe 不在 PATH` | 不是错误：cmake 会自己找 VS 生成器。真正编译失败时从「x64 Native Tools Command Prompt」运行，或装 VS2022 Build Tools（使用 C++ 的桌面开发） |
| 编译器报中文乱码 | MSVC 下保留 `/utf-8`（脚手架与示例的 CMakeLists 都带） |

### 8.2 校验失败：错误码表（MM001–MM012）

| 码 | 含义（校验器原文口径） | 处理 |
| :--- | :--- | :--- |
| `MM001` | 不是合法 zip / 无法读取 | 确认文件确实是 `.mm`（zip 容器），没被截断 |
| `MM002` | 缺 `manifest.json`，或读取失败 | 包根必须有 `manifest.json` |
| `MM003` | manifest 不是合法 JSON，或字段非法（缺 `schema`、`kind` 不是 effect/instrument、参数 id 重复/`max<=min`/`default` 越界/`curve` 非法/`steps<0`、`commands[].name` 非法） | 按 §3 逐项核对 |
| `MM004` | `schema` 高于宿主支持（当前上限 1） | 升级 MelodyMate，或把 `schema` 降到 1 |
| `MM005` | `api` 与宿主 ABI 不匹配（必须精确等于 1） | 用与 SDK 同版本的宿主；不要手改 `api` |
| `MM006` | `id` / `name` / `version` 格式非法 | `id` 反向域名小写、`name` ≤64 字符、`version` 形如 `1.0.0` |
| `MM007` | `entry` 缺失、指向的库不在包内、或指向目录 | 确认 `bin/<entry>` 真的被打进包 |
| `MM008` | 路径穿越：`../`、绝对路径、盘符、未规范化路径（包内条目、`entry`、`ui.entry` 都查） | 包内路径一律用 `/`，不用 `..` |
| `MM009` | 未知能力（fail-closed）或能力重复声明 | 只能写 §5.1 的四个值，且不重复 |
| `MM010` | 哈希算法不是 sha256、哈希值不是 64 位 hex、声明文件不在包内、内容不匹配 | 让 `mm build` 重算，别手写哈希 |
| `MM011` | 文件数超过 4096，或解压后体积超过 512 MiB | 精简包内容 |
| `MM012` | `ui.entry` 为空 / 不在包内，或 `ui.width`/`height` 越界（合法区间 0~4096） | 核对 `ui` 字段 |

`mm validate` 会一次列全部问题并返回退出码 1；`mm inspect` 会先打印摘要、再列出问题（并明确写「该包存在校验问题（不可安装）」）。

### 8.3 装载失败（引擎侧原文）

| 现象 | 含义 |
| :--- | :--- |
| `MM 插件库不存在: <path>` | `path` 指错了 |
| `无法加载 MM 插件库（可能位数不匹配或缺少依赖）: <path>` | `dlopen` 失败：位数/依赖问题 |
| `不是 MM 插件：缺少导出符号 mm_get_factory` | 没写 `MM_DECLARE_PLUGIN`（或编译成了别的 target） |
| `MM 插件 ABI 不匹配（插件 X，宿主 1）` | SDK 版本与宿主不一致 |
| `MM 插件 create() 失败` | 构造函数里抛异常/失败 |
| `MM 插件 create()` 之前的路径校验失败 | 库不在 `Library/plugins/` 下（含符号链接解析后的复核），或未配置插件目录 |

### 8.4 插件崩了会怎样

- **主程序不受影响**：MM 插件与宿主同进程，这里的"进程"是**引擎进程** `melodymate-engine.exe`（由 Go 侧 supervisor 拉起、心跳监控）。插件让引擎崩掉时，supervisor 捕获会话失效并**自动重启引擎**；音频短暂中断，工程数据与主程序存活（这条机制同时也是 [MM插件规范](MM插件规范.md) §10 验收 ④ 与 [引擎IPC协议](引擎IPC协议.md) §4.15 的崩溃隔离口径）。
- **重启节奏**：连续快速失败退避 `500ms × 次数`、封顶 **3s**；连续 **10** 次后进入失败态并停止自动重启（`内部引擎连续崩溃超过 10 次，已停止自动重启`），需要用户重新启用引擎或重启应用。会话在 Ready 后存活超过 **30s** 视为健康，失败计数清零。
- **日志位置**：
  - 主程序日志：`output/melodymate.log`（`config.LogFile`，slog 文本格式，控制台与文件双写）；
  - 引擎输出：`output/engine.log`（supervisor 把引擎 stdout/stderr 重定向到这里；插件通过 `HostServices::log` 写的 `[mm] ...` 也落在这里，因为引擎把 JUCE Logger 指向 stderr）。

### 8.5 卸载/删除时的文件占用

插件仍在 FX 槽或轨道上时，Windows 会锁住 DLL。卸载接口（`POST /api/engine/plugins/uninstall`）会先读宿主状态（`plugin.state` 的 `instruments` / `effects`，MM 条目的 `id` 就是插件 id、`format` 是 `mm`），把占用它的槽位逐个卸掉，再删安装目录，最后清注册表条目；对"占用类"错误做短重试（每次 300ms，最多 14 次 ≈ 4 秒）；仍然失败返回 **409** + `删除失败（插件可能仍被引擎占用，请先在混音台卸载并稍后重试）`。引擎侧的实例是延迟释放的（旧实例进释放队列、由消息线程回收），所以卸载后稍等片刻再删才能成功。

---

## 9. 发布与安全

### 9.1 哈希

- `mm build` 每次都计算产物库的 sha256 并写入 `manifest.json` 的 `hash.files`（默认 `sha256`）；
- 校验器逐文件复核：算法只认 `sha256`，值必须是 64 位 hex，声明的文件必须在包内，内容必须一致（否则 `MM010`）；
- 缺 `hash` 或 `files` 为空 = "未校验"：`InstallPreview.HashVerified` 为 `false`，安装界面显示「未声明哈希」（不是拒绝安装）；
- 安装目录里的 `manifest.json` 保留 `hash` 字段，注册表条目的 `hash` 取其中一个代表性值（`mmHashOf` 取 map 里第一个），详情弹窗「哈希」一行展示。哈希是**完整性比对**，不是签名。

### 9.2 能力清单

安装前必须让用户看到并逐项勾选（见 §5.2）。勾选结果只影响这一次安装的落盘决定；注册表记录的是 `capabilities`（声明清单）。勾选 UI 不做"默认全选"——`renderMMCaps` 生成的复选框初始全部未勾选。

### 9.3 ✨AI 徽标

链路是：`manifest.author.type = "ai"` → 校验器 `Manifest.IsAI()` → 同步注册表时 `origin = "ai"` 且写入 `ai.createdBy = "mm-plugin"` → 前端 `SoundRegistry.isAI(src)` 为真 → 显示 `.snd-badge-ai`（`✨AI`）。所以**AI 生成的插件请务必把 `author.type` 写成 `ai`**，这既是识别也是告知。

### 9.4 签名现状

**当前没有代码签名**：`.mm` 包与主安装包都不签名，宿主也不校验任何 Authenticode 签名。仓库里与之相关的事实只有两处：[审计报告](audit-audio-update-2026-09-05.md) L10 记录「安装包无代码签名，sha256 与二进制同信道」并把"接入代码签名"列为后续项；[发布指南](发布指南.md) 里的"签名"指 R2 存储的 S3 请求签名，与插件包无关。因此当前的信任手段就是：能力清单 + 用户确认 + 哈希记录 + 引擎受监管重启。

### 9.5 安全定位

`.mm` 里的 DLL 是**本地代码**，安装它等同安装一个程序。宿主不做沙箱（同进程加载，做不到），所以：安装必须由用户在界面上确认能力清单；AI 不能自己声明授权（§10）；插件崩溃靠"独立引擎进程 + 看门狗重启"限制影响面；插件界面跑在 `sandbox="allow-scripts"` 的 iframe 里，经消息桥与宿主通信，非法消息被丢弃。

---

## 10. AI 协作开发：MCP 工具链与"用户确认闸门"

### 10.1 六个工具（`internal/mcp/mm_tools.go`）

| 工具 | 必填参数 | 行为 |
| :--- | :--- | :--- |
| `mm_create_plugin` | `id`、`name`、`kind`（`effect`/`instrument`） | 在 `<数据根>/Library/plugin-src/<id>/` 下调 `mm new` 生成脚手架；已存在 `manifest.json` 时返回「插件工程已存在」提示，不覆盖 |
| `mm_write_file` | `id`、`path`、`content` | 写入工程内的相对路径；拒绝绝对路径、`..`、盘符；单文件上限 **512 KiB** |
| `mm_request_build_approval` | `id`、`tool`（`mm_build_plugin`/`mm_install_plugin`）、`reason` | 向用户发起界面确认：签发一次性 `approval_id`，并推 `approval_request` 事件渲染批准卡（含文件清单与实际命令）；工程不存在时直接报错 |
| `mm_build_plugin` | `id`、`approval_id` | 消费批准令牌后调 `mm build`；**25 秒**内完成直接返回日志，否则转后台作业并返回作业号 |
| `mm_build_status` | `job` | 查看后台编译的阶段（配置/编译/收集/打包/校验）、已用时与日志尾部（作业记录在进程重启后清空） |
| `mm_install_plugin` | `id`、`approval_id`（`grant` 可选） | 消费批准令牌后调 `mm install --dest <Library/plugins> [--grant …]`；成功后扫描安装目录同步注册表，并回报 `kind=mm`、`origin`、以及是否带 AI 徽标 |

另有三个与插件相关、但不在 `mm_tools.go` 里的工具（`internal/mcp/arrange_tools.go`）：

| 工具 | 必填参数 | 行为 |
| :--- | :--- | :--- |
| `plugin_list_params` | `track`（序号/id/名称，或 `master`）、`slot`（省略 = 该轨乐器） | 列出该插件实例的参数表，**VST3 与 MM 共用**；`value` 是 0~1 归一化值 |
| `plugin_set_param` | 同上 + `param`（参数名或序号）、`value`（0~1） | 写参数并返回该参数的新值与文本 |
| `mm_list` | — | 列出 `Library/plugins` 下已安装的 MM 插件（版本、能力清单、作者是否为 AI） |

工具返回的编译日志会被截尾到最近 **6000** 字符，避免长日志灌进对话。`mm.exe` 的定位顺序：环境变量 `MM_EXE` → 主程序同目录/`tools/`/`output/`/上一级 `output/` → PATH。

### 10.2 用户确认闸门：`approval_id` 才是授权

流程（`internal/mcp/approval.go`）：

```
模型 mm_request_build_approval(id, tool, reason)
   → ApprovalStore.Issue(approval_id, {tool, argsDigest, files, command, reason})
   → 前端落一张批准卡（approval_request 事件）
用户点「允许一次」 → ApprovalStore.Approve(approval_id)
模型带 approval_id 调用 mm_build_plugin / mm_install_plugin
   → ApprovalStore.Consume(approval_id, tool, argsDigest) 校验：
        · id 存在且未被消费（一次性）
        · 未过期（ApprovalTTL = 10 分钟）
        · 已获用户批准
        · 工具名与批准时一致
        · 参数摘要与批准时一致（ArgsDigest：把参数按键名排序后 JSON 序列化再 sha256，忽略 approval_id 本身）
   → 通过才执行；否则返回拒绝原因
```

关键性质：

- **一次性**：消费即作废；用户拒绝会删除令牌；
- **绑定参数**：批准的是"这一组具体参数"（工程 id、以及安装时的 `grant`），换参数不能复用；
- **绑定操作**：批准 `mm_build_plugin` 的令牌不能用于 `mm_install_plugin`；
- **不跨会话**：令牌表在进程内，重启即清空；没有确认通道时（例如 stdio MCP）`Issue` 直接报「当前没有可用的用户确认通道」，宁可拒绝执行；
- 安装与编译**各自**需要一次确认：先 `tool=mm_build_plugin`，装的时候再 `tool=mm_install_plugin`。

### 10.3 为什么 `confirm=true` 不再是授权

`approval.go` 的文件头注释写明了原因：`mm_build_plugin` 会在本机执行 C++ 构建（仅 cmake 的配置阶段就能执行命令），`mm_install_plugin` 会把原生库装进 `Library/plugins` 并在下次加载时执行其代码。此前二者的授权来自**模型在工具参数里填的 `confirm=true` / `grant=...`**——那是模型的自我声明，不是用户的授权，间接提示词注入即可驱动本机编译。现在授权只能来自 `ToolEnv.Approval`（由对话层注入、模型无法伪造）签发的一次性批准令牌；`internal/mcp/executor.go` 的结构体注释保留了这条口径：「工具参数里的 confirm/grant 只是模型的自我声明，不构成授权」。

因此：**AI 不要试图靠 `confirm: true` 或自己写 `grant` 来跳过确认**；正确做法是先调 `mm_request_build_approval`，结束本轮回复等用户在界面上点允许，下一轮再带 `approval_id` 调用。

---

## 相关文档

- [MM插件规范](MM插件规范.md) —— 包结构、manifest schema、能力模型、UI 桥、校验错误码的规范文本
- [声音管理设计](声音管理设计.md) —— 装好之后注册表与设置页的实现细节
- [引擎IPC协议](引擎IPC协议.md) —— `plugin.*` 方法组与插件装载的健壮性口径
- [前端设计必读](前端设计必读.md) —— 承载窗与界面动效必须遵守的曲线与降级规则
- [编曲窗口设计文档](编曲窗口设计文档.md) —— 机架/混音台里怎么使用装载好的插件
- [发布清单](发布清单.md) / [发布指南](发布指南.md) —— 发行包里 `mm.exe` 与 SDK 的落位要求

---

## 11. 验收清单

以下命令都在本仓库根目录执行（PowerShell）。带 ✅ 的是本文写作时实测通过的命令。

**① 工具链与脚手架**（✅）

```powershell
go run ./cmd/mm doctor                                  # 期望末行「工具链就绪，可以 mm new / mm build」，退出码 0

$demo = Join-Path $env:TEMP "mm_demo"
go run ./cmd/mm new $demo --kind effect --name "Doc Demo" --id com.example.docdemo
Get-ChildItem -Recurse -File $demo | Select-Object -ExpandProperty Name
# 期望 5 个文件：CMakeLists.txt / manifest.json / README.md / plugin.cpp / index.html
Get-Content (Join-Path $demo 'manifest.json')           # entry 应为 win-x64/com_example_docdemo.dll
Remove-Item -Recurse -Force $demo
```

**② 校验与查看已打包示例**（✅ 无需编译器）

```powershell
go run ./cmd/mm validate "engine/mm-sdk/examples/gain/com.melodymate.example.gain-1.0.0.mm"
# 期望：[mm] 校验通过: MM Example Gain 1.0.0（effect，3 个文件）

go run ./cmd/mm inspect "engine/mm-sdk/examples/filter/com.melodymate.example.filter-1.0.0.mm"
# 期望：打印 id/entry/capabilities（无：纯 DSP）/ui 360x180/3 个参数 + 4 个文件
```

**③ 编译一个真实插件**（需要 CMake + MSVC，见 §1.2）

```powershell
go run ./cmd/mm build engine/mm-sdk/examples/gain
# 期望：[mm] 已生成 …\com.melodymate.example.gain-1.0.0.mm（…KB，校验通过）
```

**④ 校验器与安装门禁的单元测试**（✅）

```powershell
go test ./internal/mm/... -count=1                      # 期望 ok  melodymate/internal/mm
go test ./internal/mm/... -run "TestInstall" -v -count=1 # 能力门禁与路径穿越两条负向用例
go test ./internal/mcp/... -run "TestMM|TestApproval" -v -count=1   # 确认闸门与工具校验
go test ./internal/soundlib/... -run "TestSyncInstalledMM" -v -count=1
```

**⑤ 安装到数据根并入册**（会真的写 `Library/plugins/`，验收后在设置页卸载清理）

```powershell
go run ./cmd/mm install "engine/mm-sdk/examples/gain/com.melodymate.example.gain-1.0.0.mm" --dest Library/plugins
# 期望：[mm] 已安装到 …\Library\plugins\com.melodymate.example.gain@1.0.0
```

命令行安装**只解包到磁盘**，注册表条目由客户端写入：在设置页「声音管理」点「↻ 同步已安装插件」（`POST /api/engine/plugins/sync`），然后：

```powershell
Select-String -Path Library/registry.json -Pattern '"id": "com_melodymate_example_gain@1.0.0"'
# 期望：命中一行，条目里能看到 "kind": "mm" 与 "classUid": "com.melodymate.example.gain"
```

带能力的包可以顺带验证 fail-closed（示例包没有能力，下面这条会以"多授权"被拒）：

```powershell
go run ./cmd/mm install "engine/mm-sdk/examples/gain/com.melodymate.example.gain-1.0.0.mm" --dest Library/plugins --grant network
# 期望：--grant 包含插件未声明的能力 "network"（不允许多授权）
```

**⑥ 引擎里的真实发声与音频路径**（需要 `bin/melodymate-engine.exe` 与可用音频设备；同一条命令在 [发布清单](发布清单.md) §3 已作为发版判据）

```powershell
go build -o engine_smoke.exe ./tools/engine_smoke
.\engine_smoke.exe `
  -mmgain   "engine/mm-sdk/examples/gain/bin/win-x64/mm_example_gain.dll" `
  -mmfilter "engine/mm-sdk/examples/filter/bin/win-x64/com_melodymate_example_filter.dll" `
  -mmsynth  "engine/mm-sdk/examples/synth/bin/win-x64/mm_example_synth.dll"
# 期望：MM 效果器装入 FX 槽 / 引擎经 MM ABI 读到自报参数 / MM 乐器实测轨道电平 > 0.001 / 滤波器三种截止频率的电平 A-B / 末行「冒烟测试全部通过 ✅」
```

**⑦ 客户端链路**（桌面版）

1. 设置 → 声音管理 → 顶部**「安装 .mm 插件」那一行**（不是「＋ 添加 ▾」下拉里的同名项，后者当前是引导占位）填 `Library/plugin-src/<id>/<id>-<ver>.mm` 或 `Library/plugins/…mm` → 「预览能力」→ 勾选 → 「安装」；
2. 或命令行 `mm install` 之后点「↻ 同步已安装插件」（`POST /api/engine/plugins/sync`）；
3. MM 分组出现条目：带 kind 徽标 `MM`、来源、能力、哈希与（AI 插件）`✨AI`；
4. 点 🎛 打开承载窗，底部「参数」面板能改参数、状态位显示「插件已就绪」；
5. 装载：`POST /api/engine/plugins/load` 带 `{"format":"mm","path":"<安装目录里的 dll>","track":0}`（效果器加 `"slot":0`，主总线用 `"master":true`）。

```powershell
# 服务在跑时可以先拿启动令牌，再查注册表（/api/bootstrap.js 是免令牌端点，返回 JSON）
$t = (Invoke-RestMethod http://127.0.0.1:7860/api/bootstrap.js).token
Invoke-RestMethod -Headers @{ "X-MM-Token" = $t } http://127.0.0.1:7860/api/sound/sources |
  Select-Object -ExpandProperty sources | Where-Object kind -eq "mm"
```

**⑧ 界面静态契约**（✅ 无需服务）

```powershell
npm run check:sound      # 期望末行「结果: 全部通过」（声音管理面板与 CSS/无障碍契约）
```
