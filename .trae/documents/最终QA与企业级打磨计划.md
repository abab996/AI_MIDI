# 最终全面 QA 与企业级打磨计划

## 任务概述

对 AI_MIDI-go 做收官级全面检查：确认无 bug、用户体验达到企业级桌面端水准，修复所有发现的问题，最终打包交付。

## 现状分析（Phase 1 探索结论）

今日已完成 8 个提交（M3 引擎 → 报告 → P0 修复 → 双入口落地 → 应用窗口布局 → FL 惯例交互对齐）。三轮探索代理 + 人工验证的结论：

### 已确认健康的项
- Go 后端：vet/test 全绿（含 panic 中间件/上报/版本端点的 5 个新单测）
- arrange.js 延迟克隆：多选克隆后 `selectedClips` 整体替换、`forEachSelectedClip` 同步移动，符合 FL 惯例；跨轨移动引用一致
- pianoroll.js 快照机制：`gs.snapshot` 仅在 move/resize/clone 手势设置，其他 dragState 类型（slice_line/erase_sweep/select_box）为 undefined 自动跳过，无串扰
- 拖放剪影：`updateDropGhost` 有 `!ghost.parentNode` 防御，renderTracks 后自动重建；dragleave/drop 双路清理
- 任务轮询：setTimeout 递归非 setInterval，visibilitychange 感知，无泄漏
- undo 后 `selectedNotes = []` 已重置，无悬空引用

### 发现的问题（按严重度）

| # | 问题 | 位置 | 严重度 |
|---|---|---|---|
| 1 | `testToneBtn` fetch 无 catch，网络失败时音状态与 UI 静默失同步 | frontend/settings.html:345-352 | Medium |
| 2 | 两处原生 `alert()` 弹窗（硬件面板不支持/失败提示），不符合产品自定义弹窗规范 | frontend/settings.html:321/326 | Medium（UX 一致性） |
| 3 | `capturingWriter` 未透传 `http.Hijacker`/`CloseNotifier` 接口，理论上影响需要连接级操作的中间件链（当前无实际使用方，属企业级完备性） | internal/server/router.go:38-60 | Low |
| 4 | 编曲窗下拉菜单（arrSnapMenu 等）在 `.arr-stage overflow:hidden`（今日布局改造引入）下是否被裁剪——**未验证**，需实测 | frontend/style.css:3515-3522 + arrange.js snap dropdown | 待验证 |
| 5 | 新增 CSS 组件（arr-drop-ghost/ws-manual-toggle/stamp-plain）在 parchment 主题下对比度未验证 | frontend/style.css | 待验证 |

## 执行步骤

### Step 1 — 修复已发现问题

1. **settings.html 测试音按钮**（#1）：
   - `testToneBtn` fetch 链加 `.catch`（失败时回滚 `toneOn` 状态 + `UI.toast` 提示）
   - 注意：settings.html 加载了 app.js，`UI.toast` 可用
2. **原生 alert 替换**（#2）：两处 `alert()` 改为 `UI.toast(..., "warn"/"err")`（错误信息保留原文案）
3. **capturingWriter 接口透传**（#3）：实现 `Hijack()`/`CloseNotify()` 方法透传给内层 ResponseWriter（带 ok 断言，内层不支持时返回错误）——不改变现有行为，只补全接口完备性

### Step 2 — 全量自动化验证

1. `go vet ./...`
2. `go test ./... -count=1`（全量非缓存）
3. `go test -race ./internal/server/ ./internal/chat/`（竞态检测，验证并发路径）
4. `node --check` 全部 14 个前端 JS 文件
5. `wails build` 成功

### Step 3 — 浏览器 E2E 冒烟（browser 代理，browser 模式 7863 端口）

关键用户旅程逐项验证：
1. **冷启动重定向**：访问 `/` 应跳转 chat.html；点"快捷操作"返回 index 正常展示
2. **快速任务表单**：打开弹窗→四类任务切换字段显隐正确→空必填校验 toast→Esc 分层关闭
3. **档案库布局**：pageScrollable=false；卡片网格内部滚动
4. **编曲窗**：展开→下拉菜单（snap 菜单）打开**不被裁剪**（验证问题 #4）→ 拖动 MIDI 文件入轨道显示剪影（含小节标注）→ 松手 clip 落在剪影位置
5. **钢琴窗**：从编曲窗双击 MIDI clip 打开→Shift+拖动音符克隆（HUD"⧉ 克隆拖动"）→ Ctrl+Z 撤销回滚→移动音符后 Ctrl+Z 可撤销（今日修复项）
6. **双主题**：切暖纸/暗蓝，检查 ghost/toggle/stamp-plain/弹窗对比度（验证问题 #5），截图
7. **设置页**：About 版本卡片显示 v3.0.0；测试音按钮点击无 JS 错误

### Step 4 — 修复 E2E 发现的问题（如有）

按发现项现场修复，重新构建复验。

### Step 5 — 收尾

1. 全部改动提交（`fix: 最终 QA 修复` / `chore: QA 收尾`）
2. `wails build` 最终打包，替换根目录 `AI_MIDI.exe`
3. 汇报 QA 结果清单（验证项 × 通过状态 + 修复项列表）

## 假设与决策

- "企业级水准"以《软件洞察报告》P0/P1 已落地项 + 本轮 QA 全绿为标准；P2 项（自动更新/CI 产物发布等）不在本轮范围
- E2E 用 `-browser` 模式代理验证（原生窗口无法在沙箱内创建，已验证该模式与原生模式共用同一 router/静态资源）
- 测试期间创建的临时项目在收尾时清理
- 若 E2E 发现 Critical 级问题，修复优先于收尾打包

## 验证方式

1. Step 2 全部命令退出码 0
2. Step 3 七项旅程全部通过（含截图证据）
3. 最终 exe 构建时间戳更新，双击可用
4. 工作树干净（全部提交）
