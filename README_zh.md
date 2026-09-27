# 🎹 MelodyMate — AI 编曲助手

> [**English**](README.md) | **中文**

**MelodyMate** 是一个「本地优先」的音乐创作辅助应用：只需要一个 API Key，AI 就能帮你配和弦、翻译歌词、设计转音、生成旋律——还能在一个 FL Studio 风格的编曲窗里完成编排和导出。所有项目、文件与对话都只存在于你自己的电脑上。

> v3.0.0 起，MelodyMate 从网页版升级为**独立桌面应用**：安装即用，无需配置 Python 环境。

---

## ✨ 功能一览

### 💬 多轮对话（主工作台）
- **档案库 + 工作台**双视图：像管理文件夹一样管理你的创作项目
- **AI 直接操作 MIDI**：说一句"写一段 8 小节 C 大调、带切分节奏的旋律"，AI 会先查内置乐理知识库，再自动生成文件、整理工程——每一步都清晰可见、随时可撤销
- **工作区绑定**：关联本地文件夹，文件双向同步
- **撤销 / 回收站**：所有文件操作都能回退
- **思维过程可视化**：AI 的推理与工具调用全程实时可见
- 项目草稿自动保存；暖纸 / 暗蓝双主题

### ⚡ 快速任务（一站式处理）
- **配和弦**：为旋律自动配置和声
- **翻译歌词**：让译文贴合旋律音节（支持日语假名）
- **设计转音**：生成花腔与装饰音
- **自定义需求**：自由输入，可输出为 MIDI

### 🎚 编曲窗（v3 新增 · 早期测试版）
- FL Studio 风格时间线：轨道、Clip 拖拽、吸附、切片、循环区间
- 双击 Clip 进入钢琴卷帘编辑音符
- **输出机架**：更换音色、导入 SF2 音色库、拖拽分配音源
- **全局 BPM**：改一处处处生效；AI 新生成的 MIDI 自动跟随（已有文件保持原速不变）
- 一键导出 **WAV**，尾音完整不截断
- 编排数据自动保存

> ℹ️ 早期测试版：暂不支持 VST 插件与 AI 联动，欢迎反馈问题。

### 🎛 v3.2 新增 —— 三层音源体系（P2–P6）

- **乐器箱（原输出机架）** —— 轨道音源只能来自机架；编排模型升级到 v2
  （`rack` / `mixer` / `pianoRoll`），界面与 AI 共用同一套 ops API，
  AI 改动不会覆盖本地未保存的片段编辑。
- **素材库（三页签）** —— 采样 / 音源 / 效果器，波形真渲染、按包筛选、键盘可达。
- **混音台 v1** —— 每轨一条通道条 + 主通道，推子（dB/ARIA）、声像、M/S、
  每通道 8 个 FX 槽、电平表，`F9` 开合。
- **VST3 宿主** —— 扫描/装载/卸载插件、读写参数、打开插件原生界面；
  乐器与效果器都支持。
- **MM 插件平台** —— 自带插件格式（`.mm`）：SDK、校验器、一键编译安装、
  沙箱隔离的插件界面承载窗、能力授权（fail-closed），以及 **AI 闭环**：
  在对话里让助手创建 → 编译 → 安装插件。
- **插件注册表** —— 已安装的 `.mm` 出现在 设置 → 声音管理，带能力清单、
  哈希校验与 ✨AI 徽标。

### 🧰 v3.2 新增命令

```bash
# MM 插件工具链（构建：go build -o mm.exe ./cmd/mm）
mm new <目录> --kind effect|instrument --name "我的效果器" --id com.example.myfx
mm build <目录> [--sdk <mm-sdk>]     # 配置 + 编译 + 写哈希 + 打包 + 校验
mm validate <包.mm> | mm inspect <包.mm>
mm install <包.mm> --dest <Library>/plugins [--grant files,network]

# 前端 / 引擎检查
npm run build                 # tsc --noEmit + 产出前端 JS
npm run check:rack            # 乐器箱（含音源选择器）
npm run check:material        # 素材库
npm run check:mixer           # 混音台
MM_BASE=http://127.0.0.1:端口 npm run check:ui   # 浏览器级走查

# 引擎冒烟（协议/设备/合成器/fx+混音/VST3/MM/SF2/走带/重连）
go build -o engine_smoke.exe ./tools/engine_smoke
.\engine_smoke.exe -vst3dir <目录> -mmgain <gain.dll> -mmsynth <synth.dll>
```

数据根结构：`Library/soundfonts`（SF2）、`Library/plugins/<id>@<版本>`（已装 MM 插件）、
`Library/plugin-src/<id>`（插件源码工程）、`Library/registry.json`（音源注册表）。
### 🔊 音频引擎（Windows 版）
- 内置 JUCE 原生引擎：ASIO 低延迟监听、实时电平表、离线渲染导出
- 未安装引擎时自动降级浏览器音频（功能完整，延迟略高）

---

## 🚀 快速开始

### Windows（推荐）
1. 从 [Releases](../../releases) 下载最新的 `MelodyMate_Setup_<版本>_windows_amd64.exe`，双击安装（免管理员权限）
2. 首次启动阅读使用声明，然后打开 **设置** 页填入 API Key
3. 新建档案，开始创作！

### Linux
1. 从 [Releases](../../releases) 下载最新的 `MelodyMate_v<版本>_linux_amd64.tar.gz` 并解压
2. `chmod +x RUN.sh && ./RUN.sh`（桌面模式需要 webkit2gtk；或 `./MelodyMate -browser` 走浏览器模式）
3. Linux 版使用浏览器音频合成，无需引擎

### 首次配置
打开 **设置** 页填入 API Key 即可使用。默认接入 DeepSeek，也支持任意 OpenAI 兼容服务（OpenAI、Gemini、本地大模型等）。

| 配置项 | 说明 |
|---|---|
| **API Key** | 你的密钥（只存在本机 `settings.json`，绝不上传） |
| **Base URL** | 默认 `https://api.deepseek.com`；可换任意兼容服务 |
| **Model** | 默认 `deepseek-v4-pro`；点"刷新"可获取可用模型列表 |

---

## 🛠 从源码构建（开发者）

环境要求：**Go ≥ 1.27**、**Wails v2**（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）、Windows 10+（Linux 桌面模式需 webkit2gtk）。

```bat
git clone https://github.com/abab996/AI_MIDI.git
cd AI_MIDI
wails build        :: Windows，产物 buildin\MelodyMate.exe
RUN.bat
```

Linux：`go build -o MelodyMate . && chmod +x RUN.sh && ./RUN.sh`。架构与设计文档见 [`docs/`](docs/)。

改前端 TypeScript（`frontend/src`）时需要 **Node.js ≥ 20**。在仓库根目录执行 `npm install` 和 `npm run build`，并把擦除后的 `frontend/js` 一起提交。只改 Go 时，上面的 `wails build` / `go build` 仍然直接可用。

---

## 📖 使用指引

### 快速任务
1. 在对话页顶部点 **⚡ 快速任务**
2. 拖入 MIDI 文件 → 选任务类型 → 填写要求 → 开始
3. 完成后直接下载结果

### 多轮对话
1. 档案库 **＋ 新建档案**
2. 用自然语言描述目标，例如 *"给这段旋律配上周式和声"*
3. AI 会先读乐理库再动手，每一步清晰可见；不满意就撤销重来

### 编曲窗
1. 工作台点 **▦ 编曲窗** 进入
2. 把左栏 MIDI 拖到轨道上，或在输出机架里更换音色
3. 调整全局 BPM、排布 Clip，点 **⬇ WAV** 导出成品

### 小贴士
- 右上角可切换暖纸 / 暗蓝主题
- 切换项目时草稿自动保存，回来接着写
- 编曲窗数据自动保存；重要工程仍建议定期备份整个项目文件夹

---

## 🔒 隐私与安全

- **API Key** 只保存在本机 `settings.json`——不进代码、不进仓库、不上传
- **所有项目数据**（对话、MIDI 文件）都只存在于你的电脑
- 密钥只会发往你在设置里填写的服务地址
- 软件完全本地运行，不收集任何使用数据

---

## 📄 许可

代码基于 [Apache License 2.0](LICENSE) 开源。
Windows 安装包内置的音频引擎（`melodymate-engine.exe`）为闭源组件，随安装包分发。

开发者：架构与设计文档见 [`docs/`](docs/)。

---

**MelodyMate** — 让 AI 做你的乐理助手。🎵
