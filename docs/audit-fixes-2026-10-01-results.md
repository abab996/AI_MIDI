# MelodyMate F01–F30 修复与验收结果

2026-10-01，主仓库与独立 `engine/` 仓库均位于 `codex/fix-audit-2026-10-01`。审查基线分别为 `78e4088`、`1388692`。原审查报告、上传的更新说明和用户已有 `.zcodeignore` 保留；修改尚未提交或推送。

**30 项缺陷均已修复并通过对应行为验收。** 验收范围为 Windows amd64、真实 Chromium/WebView2、MM SDK Gain/Synth、自建无授权依赖的 VST3 Gain/Synth。下表区分函数回归、HTTP 集成和实际原生运行，源码字符串检查不作为关闭依据。

## 逐项结果

| 编号 | 修复结果 | 通过的行为证据 | 状态 |
| --- | --- | --- | --- |
| F01 | VST FX prepare 预分配声道与块缓冲 | `engine-audio`：真实 VST Gain 将 0.1 样本变为 0.2 | 已关闭 |
| F02 | 已装载槽可更新参数，wet/旁通正确下发 | `engine-audio`：wet=0.5 得到 0.15，旁通和全干得到 0.1；加载引用的 setChain 成功 | 已关闭 |
| F03 | MIDI/ZIP/WAV 统一认证 fetch、Blob 下载并释放 URL | 后端 HTTP-DOWNLOAD；真实浏览器完整下载 31,281,186 字节 SF2，URL 创建/释放各一次；WAV 原生导出通过 | 已关闭 |
| F04 | 加载、保存、ops、SSE、预览与插件恢复统一工程代际 | 真实浏览器延迟实际 HTTP 加载/ops/保存回执，切换后仍保持目标工程轨道；save/audio/lifecycle 回归 | 已关闭 |
| F05 | UI ops 等待本地单飞保存，远端更新进行三方合并 | save 回归：未保存新增轨道/剪辑保留、ops 等待保存、重排期间目标 ID 不变；真实浏览器冲突恢复 | 已关闭 |
| F06 | 全量 PUT 必须携带 rev，项目锁内 CAS，过期返回 409 与快照 | `TestArrangementRejectsStaleAndMissingRevision`、STALE-PUT；浏览器本地/服务端冲突双方进入 IndexedDB | 已关闭 |
| F07 | 实时与离线共用 MM/VST/Synth 乐器路由，导出后还原状态和采样率 | `engine-audio`：两格式实时非静音、48000Hz WAV 非静音，插件状态不变、设备恢复 44100Hz；桌面实际播放 | 已关闭 |
| F08 | 切换清空实例，重排/删除按稳定 trackId 重建映射 | 真实桌面 .2/.8 两实例重排、删除后剩一实例，空工程实例/FX 均为零 | 已关闭 |
| F09 | UI 与 AI 乐器装载持久化机架和轨道引用，FX 排除乐器 | `TestAIPluginAssignmentUsesRegisteredVersionAndStableTrack`；真实桌面 VST 扫描→机架指派→MM/VST 替换 | 已关闭 |
| F10 | 从注册表解析 MM 固定版本、SDK 入口与 VST ID/路径 | SDK legacy-entry、VST scan 单测；实际桌面 MM/VST 乐器成功加载、替换和重开恢复 | 已关闭 |
| F11 | MM 主总线目标与轨道目标明确区分 | lifecycle 回归检查 master 标记；实际桌面 master MM Gain 就绪、SDK 参数写入 .7，重开仍为 master FX | 已关闭 |
| F12 | 引擎重启重新实例化后按顺序重放参数与匹配状态，业务错误可见 | `TestAuditLiveRestartAndProjectClear`：实际强制结束子进程、自动重启、状态/FX/master=0 恢复，通过 race | 已关闭 |
| F13 | VST 乐器卸载与替换统一清理引用 | `engine-audio` 检查卸载后指针为空；实际桌面 MM/VST 替换始终只有一个目标乐器 | 已关闭 |
| F14 | MM All Notes Off 使用 B0 7B 00 | `native-dsp`、`engine-audio`：MM Synth 发声后 panic，实际样本降为静音 | 已关闭 |
| F15 | prepare 完成后发布；退休等待回调完成及 MIDI 读者退出 | `native-dsp` 检查发布顺序、完成前禁止释放、读者访问期间禁止释放、完成后回收；桌面反复装卸 | 已关闭 |
| F16 | 实例持有 HostServices，SDK 回调按实例保存 | MM Services DLL 两实例 .2/.8，prepare 返回后仍读取各自宿主回调且互不串实例 | 已关闭 |
| F17 | AI 混音变化同步引擎和独立可见混音窗口 | save/lifecycle 回归；实际桌面 Arrange 关闭时远端 master .2 更新可见控件及音频协调，零增益重开保留 | 已关闭 |
| F18 | 注册表 sourceId 经认证取得 SF2，兼容旧 IndexedDB 记录 | BROWSER-SF2-SILENT；真实浏览器解析 GeneralUser SF2 并得到预设；过期 SF2 回应不再解码或装入替代音源 | 已关闭 |
| F19 | MM 参数消息只接受目标沙箱 iframe | 正向探针拒绝外窗参数写入；真实浏览器外窗 tooltip 被丢弃；桌面目标 SDK iframe 的参数写入成功 | 已关闭 |
| F20 | 恢复顺序固定为实例→槽参数/主增益→匹配状态，显式保留零增益 | 真实桌面应用重开恢复 gain=0、wet=.25、bypass=true、稳定乐器参数 .2；引擎强制重启同样通过 | 已关闭 |
| F21 | SSE 合并所有 changed 并保留最大 rev，旧工程拉取丢弃 | 正向 SSE 探针：rack/mixer/tracks 三类事件，乱序版本 1/2/1，合并拉取最低版本为 2；三方合并回归 | 已关闭 |
| F22 | 真实保存路由支持 5MB，超限统一 413 | `TestArrangementHTTPSizeBoundary`：低于、恰好 5MB、5MB+1；ARRANGEMENT-LIMIT 的超过 2MB 请求成功 | 已关闭 |
| F23 | API 返回归一化默认值，UI 直接按归一化值恢复 | MM Gain API 默认 .5；native-dsp 与 MM-RESET-NORMALIZATION 通过 | 已关闭 |
| F24 | 注册表路径统一基于 ProjectRoot，并修复缺失 Library 的旧路径 | MM-REGISTRY-PATH、SDK legacy-entry 回归；真实桌面 SDK DLL 经注册表路径装载成功 | 已关闭 |
| F25 | 刷新保留禁用状态，重复刷新不改写注册表 | `TestMMPathAndDisabledRefresh`；真实桌面禁用测试插件、同步、应用重启后仍 disabled | 已关闭 |
| F26 | 迁移不重复吸收已管理目录，删除/禁用同步清理镜像 | PACK-DELETE-MIRROR、迁移回归；实际桌面删除采样包后重启，注册表和 material_dirs 均无残留 | 已关闭 |
| F27 | 固定版本 manifest UI 入口、限定目录资源会话、相对模块资源；桌面同源承载 | 真实浏览器 v2 自定义入口/ES module 依赖/CSS/SVG 加载正确；桌面默认 index.html 与 SDK Gain 交互通过；关闭/请求途中关闭撤销通过 | 已关闭 |
| F28 | 可信 Wails 引导；浏览器一次性授权、清除地址凭据并保存 sessionStorage | `TestBootstrapRequiresAuthorization`；未授权 HTTP 401；真实浏览器兑换/刷新/插件窗口和真实桌面可信 bootstrap 通过 | 已关闭 |
| F29 | MM/VST 实时与导出使用实际 BPM、位置、播放状态 | MM Services DLL 验证 150/8/playing、90/2/stopped；VST PlayHead 存在且值正确，导出后恢复 live transport | 已关闭 |
| F30 | 替换/卸载/关闭共用引用清理与退休路径 | `engine-audio` 连续替换 12 次 active count 始终 1，卸载为 0；MM→VST 后 MM 引用为零；桌面清空工程通过 | 已关闭 |

## 分阶段实施记录

- A：增加版本冲突 HTTP 测试、三方合并与保存协调行为测试后实现 CAS、单飞保存、ID 合并和 IndexedDB 草稿。真实浏览器验证冲突重开、Esc/Shift+Tab/Enter 和双主题，以及旧工程 HTTP 回执隔离。关闭 F04–F06、F21、F22。
- B：新增 CTest `native-dsp`/`engine-audio`、MM Services DLL 和 VST Gain/Synth 测试插件，修复实时 DSP、生命周期、卸载、panic 和 bounce。后续真实扫描暴露的临时 catalog 指针问题也已修复；VST transport 与导出恢复增加了样本/状态断言。关闭 F01、F02、F07、F13–F16、F23、F29、F30。
- C：增加稳定状态、引用替换、AI 指派和生命周期协调测试，修复稳定目标、工程切换、重启重放和业务错误。真实桌面完成重排、删除、插件替换、播放、独立混音窗口和应用重开；实际强制结束原生进程后重放通过 race。关闭 F08–F12、F17、F20。
- D：增加 SDK 旧入口、路径/禁用刷新、VST 注册、管理目录迁移回归。浏览器完成实际 SF2 解码；桌面重启确认禁用和删除不被迁移恢复。关闭 F18、F24–F26。
- E：增加 bootstrap 和资源会话 HTTP 回归，修复认证下载、消息源、资源沙箱与一次性授权。实际验收额外发现并修复 opaque-origin ES module CORS、Wails 子窗口资源拦截和默认 index.html 目录重定向；每项均补行为测试。关闭 F03、F19、F27、F28。

## 迁移与保留数据

工程迁移保存 `.before-v2-migration` 原始字节备份；注册表第一次改写保存 `.before-audit-2026-10-01`；旧索引插件状态只有轨道/格式/引用归属匹配才迁移，并保留 `.before-stable-id`。插件引用替换额外保留 `.before-reference-change` 及按引用哈希的历史状态，因此 MM→VST→MM 可恢复原 MM 状态。

`TestLegacyArrangementBackupIsOriginalAndIdempotent`、`TestLegacyStateMigrationRequiresOwnershipAndKeepsBackup`、`TestPluginReplacementKeepsBothStates`、迁移/刷新幂等测试均通过。无法证明归属的旧状态保留原文件并报告错误；损坏数据拒绝覆盖。该策略不推测旧状态属于哪个实例。

## 验收日志与复现

日志和同步产物位于 `output/audit-2026-10-01/`（Git 忽略目录），SHA-256 清单为 `artifacts.json`。

| 检查 | 结果与日志 |
| --- | --- |
| Go 全量测试 | `go test ./... -count=1` 通过，`go-test.log` |
| Go race | `go test -race ./... -count=1` 通过，`go-race.log` |
| Go vet | `go vet ./...` 通过，`go-vet.log` |
| TypeScript 与前端行为 | `npm run typecheck`、`npm test`、`npm run check:audit` 通过，`frontend-*.log` |
| 原审查 21 观察点 | 9 个后端断言 + 12 个前端行为观察均已转为通过断言；原复现源码快照保留在 `tools/audit-2026-10-01/*before-fixes.txt` |
| 原生构建与 CTest | Release 构建完成，2/2 通过；实际打包后的测试程序再次运行通过，`native-acceptance.log` |
| 浏览器 | `tools/dev/run_browser_audit.ps1` 通过，`browser-acceptance.log`，截图在 `browser/` |
| 桌面与重开 | `tools/dev/run_desktop_audit.ps1` 通过，`desktop-acceptance.log` |
| 强制退出重启 | `TestAuditLiveRestartAndProjectClear` 使用实际引擎，通过 race，`native-restart.log` |

桌面正式产物是正常 Wails production 构建。CDP 自动化使用单独的 smoke 构建，仅在隔离复制的官方 WebView2 loader 中保留 loopback 调试参数；没有修改模块缓存、主仓库 go.mod 或正式 loader。两者使用相同业务代码和前端。native 测试引擎与桌面 `bin/` 引擎的哈希一致。

未验收范围：商业第三方插件、其他操作系统、其他声卡/驱动组合以及人工听感比较。实时/离线验收确认使用相同插件路由、产生非静音样本和状态恢复，不宣称 WAV 与实时输出逐样本相同。这些范围不列入已关闭缺陷的通过证据。
