# 2026-10-04 隔离审查探针

本目录仅用于验证当前代码缺陷，不修改业务源码或用户项目。

- `go run ./tools/audit-2026-10-04`：独立临时数据根中的后端行为观察。
- `go run -race ./tools/audit-2026-10-04 --task-switch-race`：并发执行真实任务切换 HTTP handler，检查共享任务选择的竞争（当前应报告 data race 并非零退出）。
- `node tools/audit-2026-10-04/frontend-repro.mjs`：真实 JS 函数在隔离 VM 中的行为观察及 TS/JS 一致性检查。
- `node tools/audit-2026-10-04/capture-results.mjs`：依次运行上述三项，将原始 UTF-8 输出与退出码保存到 `results/`。竞争检测在首个 race 时停止。

`CONFIRMED` 表示观察到当前缺陷，不表示修复验收通过。修复时应把对应观察改为期望正确行为的回归断言。

前端探针模拟 DOM、网络、计时器和桥事件，不等同于真实 WebView/浏览器验收。后端探针调用真实 handler 与文件函数，数据全部放在独立临时目录。race 检测器强制退出时可能保留该临时目录。

审计结论与执行顺序分别见 [审计报告](D:/pyx/AI_MIDI-go/docs/audit-2026-10-04.md) 和 [修复计划](D:/pyx/AI_MIDI-go/docs/plans/2026-10-04-bugfix-plan.md)。
