# MelodyMate MM 插件规范（v1）

> 状态：P6 规范落地（v1）。实现镜像：校验器 `internal/mm/`（主程序）、运行时 `engine/Source/Plugins/`（引擎，P6 后续批次）。
> 一句话：`.mm` 是 **zip 包 + manifest + 原生 C++ 编译产物**；DSP 走原生编译（与 VST 同一条引擎音频链路），
> 插件作者只写"插件逻辑 + UI"，音频 I/O、线程、缓冲、参数同步全部由 SDK 与宿主托管。

---

## 1. 包结构

```
myplugin-1.0.0.mm                 # zip（store 或 deflate 均可），扩展名 .mm
├── manifest.json                 # 必需，唯一权威元数据
├── bin/
│   └── win-x64/myplugin.dll      # 按平台分目录的原生库（P6 只支持 win-x64）
├── ui/
│   └── index.html                # 可选：插件 UI（沙箱 iframe 承载）
├── presets/                      # 可选：出厂预设（*.json）
├── LICENSE                       # 可选
└── README.md                     # 可选
```

规则：

- 路径一律用 `/`，禁止以 `/` 开头、禁止 `..` 段、禁止盘符（防 zip-slip；校验器与解包器双查）。
- `manifest.json` 之外的字段宿主忽略（前向兼容）；未知的 `manifest` 字段按 §3 处理。
- 单包上限：压缩前 512 MiB、文件数 4096（防御 zip 炸弹）。

## 2. manifest.json

```jsonc
{
  "schema": 1,                       // 规范版本（整数，必填）
  "id": "com.example.myfilter",      // 反向域名，仅 [a-z0-9.-]，长度 3..128（必填）
  "name": "My Filter",               // 显示名（必填，≤64）
  "version": "1.0.0",                // semver（必填）
  "vendor": "Example Audio",         // 供应商（可选，≤64）
  "kind": "effect",                  // "effect" | "instrument"（必填）
  "entry": "win-x64/myfilter.dll",   // 相对 bin/ 的库路径（必填）
  "api": 1,                          // 宿主 ABI 版本，必须等于宿主支持的 MM_ABI_VERSION
  "capabilities": [],                // 能力声明，见 §4（默认空 = 只能做纯 DSP）
  "params": [                        // 参数声明（宿主据此建参数表；也可运行时动态注册）
    { "id": "cutoff", "name": "Cutoff", "min": 20, "max": 20000, "default": 1000,
      "unit": "Hz", "curve": "log", "steps": 0, "automatable": true }
  ],
  "ui": {                            // 可选
    "entry": "ui/index.html",
    "width": 600, "height": 380,     // 初始尺寸；>0 且 ≤4096
    "resizable": true
  },
  "commands": [                      // 可选：暴露给 AI 的自定义工具（§6）
    { "name": "set_cutoff", "description": "设置截止频率", "params": { "type": "object" } }
  ],
  "author": { "type": "human" },     // "human" | "ai"（AI 生成的插件据此显示 ✨AI 徽标）
  "hash": { "algorithm": "sha256", "files": { "bin/win-x64/myfilter.dll": "<hex>" } }
}
```

- **`params[].curve`**：`linear` | `log` | `exp`（宿主用于 UI 映射与 AI 取值换算）。
- **`steps`**：0 = 连续；>0 = 离散档位（宿主按档位量化）。
- **`hash.files`**：安装时逐文件校验（缺哈希 = 未校验，安装界面标黄）。

## 3. 版本与兼容策略

| 字段 | 变更规则 |
|---|---|
| `schema` | 破坏性变更 +1；宿主支持 `schema ≤ 当前`，更高版本拒绝安装并提示升级 |
| `api` | 宿主 ABI 版本；必须**精确相等**（原生库与宿主同进程，ABI 不同即崩溃） |
| `version` | 插件自身 semver；同 id 多版本可共存（`Library/plugins/<id>@<ver>/`） |
| 未知字段 | 忽略（前向兼容）；未知 `capabilities` 项**拒绝安装**（fail-closed） |

## 4. 能力模型（默认最小权限）

| capability | 编译期宏 | 运行期行为 |
|---|---|---|
| （空） | — | 只能做纯 DSP：读 MIDI/音频、写参数、访问走带信息 |
| `files` | `MM_CAP_FILES` | 读写自己的插件目录（`Library/plugins/<id>@<ver>/data/`） |
| `network` | `MM_CAP_NETWORK` | 允许出网（**默认拒绝**；安装时需用户显式勾选） |
| `threads` | `MM_CAP_THREADS` | 允许插件自建线程（默认只允许宿主的音频线程） |
| `midi_out` | `MM_CAP_MIDI_OUT` | 允许产生 MIDI 输出（琶音器/和弦器类） |

- SDK 默认**不暴露**文件/网络/线程 API；未声明的能力在编译期就没有对应头文件符号，运行期宿主再校验一次（双重拒绝）。
- 安装时必须向用户展示能力清单；勾选结果写进注册表（`capabilities_granted`）。运行期拿到的能力 = 声明 ∩ 授权，缺一即拒绝加载并给可解释错误。
- 安全定位：`.mm` 里的 DLL 是**本地代码**，等同安装一个程序。宿主不做沙箱（做不到），靠"能力清单 + 用户确认 + 哈希记录 + 引擎受监管重启"降低风险；插件崩溃不得影响主程序（引擎子进程 + 看门狗重启，P6 运行时实现）。

## 5. ABI 与宿主托管面

SDK 提供 C++17 头（`engine/mm-sdk/include/mm/`）：

```cpp
struct AudioBufferView { float* const* channels; int numChannels; int numSamples; };
struct MidiView        { const uint8_t* data; int size; };   // 3 字节消息序列
struct TransportInfo   { double bpm; double ppqPosition; bool playing; };

class Processor {
public:
    virtual void prepare(double sampleRate, int blockSize) = 0;
    virtual void process(AudioBufferView& audio, const MidiView& midi, const TransportInfo& t) = 0;
    virtual void reset() = 0;
    virtual void getState(void*& data, int& size) { data = nullptr; size = 0; }   // 可选
    virtual void setState(const void* data, int size) {}                           // 可选
};

MM_DECLARE_PLUGIN(MyProcessor)      // 导出宿主需要的工厂符号
MM_PARAM_FLOAT(cutoff, 20.f, 20000.f, 1000.f)   // 参数声明 DSL（原子 + 平滑由宿主托管）
```

托管保证：音频线程**零分配零锁**（宿主预分配缓冲）、参数读写原子且已平滑、`process` 只被音频线程调用、
状态序列化在消息线程。禁止事项由 SDK 头注释与 `mm validate` 静态检查点共同守护。

## 6. UI 桥协议（4.7 节承载窗）

插件 UI 跑在**沙箱 iframe**（`sandbox="allow-scripts"`，无 same-origin）里，与宿主 `frontend/mm-ui.html` 用 `postMessage` 通信：

| 方向 | 消息 | 说明 |
|---|---|---|
| UI → 宿主 | `{type:"mm.ready"}` | 页面就绪，宿主随后推全量参数 |
| 宿主 → UI | `{type:"mm.params", params:[{id,value,text}]}` | 全量参数（含 AI/外部改动） |
| UI → 宿主 | `{type:"mm.setParam", id, value}` | 写参数（0~1 归一化由宿主换算） |
| 宿主 → UI | `{type:"mm.transport", bpm, playing, ppq}` | 走带信息（10Hz） |
| UI → 宿主 | `{type:"mm.resize", width, height}` | 尺寸上报（宿主调整窗口） |
| UI → 宿主 | `{type:"mm.tooltip", text}` | 悬浮提示（宿主决定展示方式） |
| 宿主 → UI | `{type:"mm.theme", theme:"dark"\|"parchment"}` | 双主题令牌（UI 应据此取色） |

- 宿主校验每个消息的 `type` 与字段类型，非法消息丢弃并计数（防 UI 崩坏影响宿主）。
- 参数变化用增量消息 `{type:"mm.paramChanged", id, value}` 从宿主推给 UI（自动化/AI 改动时同步界面）。

## 7. 一键编译工具（`cmd/mm`，产出 `mm.exe`）

| 命令 | 行为 |
|---|---|
| `mm new <dir> --kind effect\|instrument --name "..."` | 脚手架（CMakeLists + src + ui 骨架 + manifest 模板） |
| `mm doctor` | 检测 VS2022 Build Tools + cmake + git，缺失给安装指引（退出码非 0） |
| `mm build` | 配置 + 编译 + 收集 `bin/<platform>/` + `mm validate` + 打包 zip + 写 sha256 |
| `mm validate <pkg.mm>` | 校验（本规范 §1–§4 全部规则） |
| `mm inspect <pkg.mm>` | 打印 manifest 摘要与文件清单（不安装） |
| `mm install <pkg.mm>` | 解包到 `Library/plugins/<id>@<ver>/` + 写注册表（需能力授权） |
| `mm test <dir>` | 离线渲染自测（本地渲染一个块，验证出声/参数生效） |

## 8. 安装目录与注册表

```
Library/plugins/com.example.myfilter@1.0.0/
├── manifest.json
├── bin/win-x64/myfilter.dll
├── data/                     # files 能力可写目录（未授权时不存在）
└── ui/index.html
```

注册表条目（`Library/registry.json`，kind=`mm`）额外字段：`pluginId`、`version`、`capabilitiesDeclared`、
`capabilitiesGranted`、`hashVerified`、`origin: builtin|user|ai`、`sourcePkg`（原始 .mm 路径）。

## 9. AI 闭环与工具（P6）

| 工具 | 行为 |
|---|---|
| `mm_create_plugin` | 脚手架 + 写入 manifest 与骨架代码（默认只给安全面模板） |
| `mm_write_file` | 写插件目录内文件（路径白名单：`Library/plugin-src/<id>/`） |
| `mm_build_plugin` | 调 `mm build`（**首次需用户确认**），返回编译日志尾部 |
| `mm_install_plugin` | 调 `mm install`，安装前展示能力清单 |
| `plugin_list_params` / `plugin_set_param` | 已装插件的参数读写（与 VST3 共用） |
| `commands` 映射 | manifest 里 `commands[].name` 变成 AI 可调用的自定义工具 |

## 10. 验收口径（P6）

1. 人类开发者按《MM插件开发指南》用 `mm new/build/install` 从零做出可发声插件；
2. AI 在对话里创建一个小合成器/效果器 → 一键编译安装 → 出现在声音管理（✨AI 徽标）→ 可加载、可调参、可开 UI；
3. 未授权能力（如 `network`）被**工具层 + 宿主**双重拒绝；
4. 插件崩溃不影响主程序（引擎受监管重启，主程序存活）；
5. AI 生成插件的识别链路（徽标 / 能力授权对话框 / 一键编译进度卡）在双主题下完整可用。

## 11. 校验器的错误码（`internal/mm`）

| 码 | 含义 |
|---|---|
| `MM001` | 不是合法 zip / 读取失败 |
| `MM002` | 缺 `manifest.json` |
| `MM003` | manifest 不是合法 JSON 或缺必填字段 |
| `MM004` | `schema` 高于宿主支持 |
| `MM005` | `api` 与宿主 ABI 不匹配 |
| `MM006` | `id` / `name` / `version` 格式非法 |
| `MM007` | `entry` 路径非法或文件不存在于包内 |
| `MM008` | 路径穿越（`..`、绝对路径、盘符） |
| `MM009` | 未知能力项（fail-closed） |
| `MM010` | 哈希不匹配 |
| `MM011` | 超过体积/文件数上限 |
| `MM012` | `ui.entry` 缺失或尺寸非法 |
