# AI_MIDI v2.0.0

v1 是 Gradio 简易 WebUI（另附 maliang 桌面 GUI 与命令行入口），界面为 Gradio 默认样式，功能只有四种快捷操作。v2 将界面全面重做为自绘前端，四种快捷操作完整保留，并在此基础上新增多轮对话、项目档案库、乐理知识库、设置页等一整套新能力。

## 界面重做

- 抛弃 Gradio，改为 FastAPI 本地服务 + 自绘前端（替代 Gradio 默认样式）。
- 三个页面：**主工作台**（快捷操作）/ **多轮对话**（项目与对话）/ **设置**，页头互跳。
- 默认 pywebview 原生窗口，`--browser` 可改用系统浏览器。
- 暖纸 ⇄ 暗蓝双主题，本地记忆。

## 新增功能（相对 v1）

- **多轮对话**：与 AI 围绕 MIDI 项目进行多轮创作对话，不再是单次问答。
- **MCP 工具调用**：AI 可以直接在项目里创建、解析、整理、删除 MIDI 文件，每一步以可折叠工具块展示。
- **乐理知识库**：内置 20 份系统化知识库（乐理基础、配和弦指南、对位、配器、曲式、调式互换等），AI 按需查阅。
- **项目档案库**：多项目管理，档案库 + 对话工作台双视图。
- **工作区绑定**：关联本地文件夹，文件双向同步；每个文件操作可撤销 / 恢复（带回收站）；跨项目全文搜索。
- **思考过程可视化**：AI 的推理内容以流式思考块实时展示，思考完毕立即收起。
- **设置页**：API Key、Base URL、模型与生成参数图形化配置并持久化到 `settings.json`（已被 git 忽略，密钥不入库）；v1 的环境变量 / `.env` 方式不再需要。支持任意 OpenAI 兼容服务商（DeepSeek / OpenAI / Gemini / 本地 LLM），可刷新模型列表。
- **流式输出 + 上下文压缩**：全程流式输出；长对话自动摘要压缩，保持上下文不炸。
- **Windows 安装包**：Inno Setup 安装，中英双语向导，卸载保留用户数据；另有 PyInstaller 多文件打包版。

## 修复

- 修复部分 bug，提升稳定性。

## 升级提醒

- 无数据库迁移：数据均为本地文件（`settings.json` / `projects/` / `output/`）。
- v1 的四种快捷操作（配和弦 / 翻译歌词 / 设计转音 / 其他要求）在 v2 主工作台中一一对应，使用方式不变。
- API Key 从环境变量迁移到设置页：升级后请在「设置」页填写一次。

**发布日期**：2026-08-06

**更新规模**：相对 v1（tag `1`）— 67 个文件已更改 | +18,363 / -2,684 行

## 下载与安装

访问 [Releases](https://github.com/abab996/AI_MIDI/releases) 下载对应版本。

| 文件 | 说明 |
|---|---|
| AI_MIDI_Setup_2.0.0.exe | 推荐 — Inno Setup 安装包，Windows 10+ x64，中英双语向导，卸载保留用户数据 |
| Source code (zip / tar.gz) | 源码运行：Python 3.10+，`pip install -r requirements.txt`，`python main.py` |

首次使用请在「设置」页填写 API Key 与 Base URL（默认 `https://api.deepseek.com`）。详见 [README_zh.md](https://github.com/abab996/AI_MIDI/blob/main/README_zh.md)。
