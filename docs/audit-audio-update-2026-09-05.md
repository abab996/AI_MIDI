# 音频引擎与自动更新专项审计 — 2026-09-05

本次审计聚焦三个方向：应用内自动更新（v3.0.3 新增）、C++ 音频引擎（JUCE）、
打包/发布链路。全部发现逐行验证后修复；**高危 6 项、中危 10 项已全部修复**，
低危项记录在 §3 待后续处理。与 `audit-2026-09-05.md`（全仓审查）互补，该文档
中已记录的 44 个低危项不在此重复。

## 验证状态

| 验证 | 结果 |
|---|---|
| `go vet ./...` | 通过 |
| `go test ./...`（含 12 个新增回归用例） | 全部通过 |
| `tools/build_engine.bat` 重建引擎 | 成功 |
| `tools/engine_smoke` 冒烟（34 项，含新增 panic 2 项） | 全部通过 |
| `tools/bounce_probe` 33 轨越界探测（新增） | 通过（修复前必崩溃） |
| `wails build` | 成功（15.9s） |

---

## 1. 已修复 — 高危

### U1. 应用内更新下载必失败（更新功能完全不可用）
`internal/server/handler_update.go:108` 把 `req.Context()` 传给
`downloader.Start()`。HTTP 请求上下文在 handler 返回时即取消，而 handler
启动下载 goroutine 后立即返回——下载 HTTP 请求瞬间被中止
（`io.Copy` 报 context canceled → StateFailed）。「立即更新」每次必失败，
唯一可用路径是浏览器降级下载。单测传的是 `context.Background()` 所以没测出。
**修复**：`Start` 改传 `context.Background()`（下载生命周期独立于请求）。
**回归测试**：`TestUpdateApplyDownloadSurvivesRequestReturn`。

### U2. 下载进度条全程 0%
`internal/update/download.go` 原在 `io.Copy` 结束后才 `setTotal(total)`，
下载全程 `Snapshot().Total == 0`，前端永远显示 "0% (x.x / 0.0 MB)"。
**修复**：`setTotal` 移到响应头校验后、写临时文件前。
**回归测试**：`TestDownloaderTotalDuringDownload`。

### A1. MidiRing SPSC 双生产者竞态（丢音符/幻音/卡音）
`MidiRing.h` 是单生产者/单消费者无锁环，但两个线程并发 push：管道线程
（实时音符，`Main.cpp onFrame`）与消息线程（bounce 内
`synths_[track]->noteOn/noteOff/allSoundOff`）。两个生产者读到同一
`head`、各写一格、head 二次 store——后写者覆盖，事件丢失且 head 可能
回退。bounce 与实时演奏并发时丢 note-off → 卡音。
**修复**：`SynthEngine` 新增 `noteOnDirect/noteOffDirect/allSoundOffDirect`
（持 `loadMutex_` 直呼 tsf），bounce 全部改走 direct 变体（前提成立：bounce
经 `bounceRequest_` 握手已独占渲染权），ring 恢复单生产者。

### A2. bounce tracks 路径轨索引越界（引擎进程崩溃）
`EngineAudioDevice.cpp` bounce 的 tracks 分支用 JSON 数组下标 `ti` 直接
落轨（`tmpBufs[ti]`，容量 32），无边界检查。编曲前端轨数不受引擎 32 轨
约束，第 33 轨带 clip 的编曲触发 `POST /api/audio/bounce` 即
`std::vector::operator[]` UB → 引擎进程崩溃（前端表现为引擎无限重启）。
**修复**：tracks 循环入口与 clip 渲染处双重边界检查（越界轨跳过）。
**验证**：`tools/bounce_probe` 实测 33 轨 bounce 不再崩溃。

### A3. 原生编曲音符系统性提前 0–150ms（MIDI/音频轨节奏错位）
`frontend/js/arrangement/audio_engine.js _triggerDue`：Web Audio 路径把
精确 `when` 传给 synth，原生路径（`EngineBridge.noteOnTrack`）却触发即发。
原生音符比采样精确的音频轨平均提前 ~75ms（note-off 提前更多，音符偏短）。
**修复**：原生路径改为 `setTimeout` 到 `ev.t` 才发；timer 集中登记，
`stopSchedule` 全部撤销（同时消除停止后的幽灵音与 B21 的 note-off 丢失窗口）。

### A4. Transport 循环回绕死码（locate 越过循环终点后永不回绕）
`Transport.h advance` 的回绕条件要求 `cur < loopEndPos`，定位到循环终点
之后播放时永远不满足——「起点在循环外」分支是空操作，播放头一路狂奔。
前端被迫用双循环数学兜底。
**修复**：改为 `next >= loopEndPos && cur >= loopStartPos` 即折回
（模语义，与前端 `_posToBeat` 一致）。

## 2. 已修复 — 中危

### U3. 停滞看门狗缺失（卡死下载永远"进行中"）
`download.go` HTTP 客户端 `Timeout: 0` 且无任何停滞检测：源站挂起时下载
状态永远 `downloading`，前端无限轮询（强制更新模式下无取消按钮）。
**修复**：`countingWriter` 刷新 `lastProgress`，1s ticker 检查——
60s 无新增字节即 cancel 并报「下载停滞超时」。看门狗先于 `http.Do`
启动（等响应头阶段同样可能挂死）。
**回归测试**：`TestDownloaderStallWatchdog`。

### U4. 清单版本号未消毒拼落盘路径
`handler_update.go` 用远程清单的 `mani.Version` 直接
`filepath.Join(...)`。清单被投毒时 `..\` 构成路径穿越写。
**修复**：`update.IsValidVersionString`（仅数字与点，逐段非空），
非法即 502 拒绝。**回归测试**：`TestIsValidVersionString`、
`TestUpdateApplyRejectsBadVersion`。

### U5. 下载目录 %TEMP% 与自家文档矛盾
发布指南 §10 记录过「%TEMP% 下的文件几秒内消失」（系统清理器）。
下载完成但启动安装失败时用户会指向已消失的文件。
**修复**：改 `os.UserCacheDir()/AI_MIDI/`。

### A5. 非原子 double sampleRate 数据竞态（UB）
`EngineAudioDevice` 曾并存 `double sampleRate` 与 `atomic<double> sampleRate_`。
音频回调写、消息/管道线程读非原子副本——撕裂读取会让 beat↔sample 换算
整体漂移（音符/素材位置错乱）。
**修复**：删除非原子副本，全部读写点（scheduleNotes/bounce/
currentSampleRate/setTimecodeSender/audioDeviceAboutToStart）统一
`sampleRate_`。

### A6. 陈旧 audioIdle_ 可跳过 bounce 握手
上一次 bounce 的空闲确认若未被后续回调块清除，下一次 bounce 直接跳过
等待——恰是握手要关闭的"回调与 bounce 并发渲染同一 tsf"窗口。
**修复**：`bounce()` 置 `bounceRequest_` 前先 `audioIdle_.store(false)`。

### A7. bounce 结束还原陈旧采样率
bounce 期间设备被 `applySetup` 重开时，回调已按新率调谐全部 synth，
bounce 结束却还原暂存的旧率 → 后续输出整体跑调。
**修复**：还原目标改 `sampleRate_.load()` 当前值。

### A8. 无 panic/allNotesOff IPC 逃生口
丢 note-off（环满丢弃、后端切换）后卡音无任何恢复手段（只能杀引擎）。
**修复**：新增 `panic` IPC 方法（管道线程执行，`panicAll` 经事件环投递），
全链接线：`Client.Panic` → `Supervisor.PanicAll` → `App.EnginePanic`
→ `POST /api/audio/panic` → `EngineBridge.panic()`（浏览器模式 HTTP 回退）。
协议文档 §4.5a 已同步。

### A9. 播放中 BPM 变更音频素材失同步
`setTempo` 只重算走带位置，不重排已调度素材（位置是旧 BPM 烘焙的采样数），
MIDI 轨跟随新速率而音频轨不跟随 → 渐进失同步。
**修复**：`arrange.js setBpm` 播放中分支追加 `rescheduleSamplesDebounced()`
（全量重建调度表，语义已存在）。

### A10. 后端切换残留原生音符
引擎状态从 ready 翻出（崩溃/重启/强制 WEBAUDIO）时，已发原生 note-on
的音符没有前端 voice 可停。
**修复**：`chat.html` 引擎徽章轮询检测到 ready→非 ready 翻转时
best-effort `EngineBridge.panic()`。

## 3. 记录未修（低危/观察项）

| # | 位置 | 问题 |
|---|---|---|
| L1 | `client.go` 管道事务模型 | 同步句柄上写请求与响应读者并发（Windows 容忍，但违反自述契约）；超时残留 goroutine 理论上可交错（已通过 markDead 收敛，见 M1 修复） |
| L2 | `client.go Timecode()` | 长 bounce 期间锁存值冻结（GetLevels 已改 TryRequest 不阻塞；timecode 锁存同样受长请求影响，UI 轮询有 pending 守卫，影响限于刷新率） |
| L3 | `smf_reader.go:284` | 通道事件 ReadByte 错误被忽略（截断文件出幻影音符）；running status 在 SysEx/meta 后未清（规范偏差）；SMPTE division 静默换 480 |
| L4 | `smf_writer.go:74` | AI 提供的 Start/End 无上界，超 2^32 tick 截断成垃圾时序 |
| L5 | `handler_audio.go` bounce | 设备采样率与导出渲染率语义混用（产品未区分，显式传参可覆盖）——已在代码注释与本表记录 |
| L6 | `MixerGraph.h:111` | 非活跃轨峰值电平不更新（界面显示陈旧值） |
| L7 | 测试音独占输出 | testTone 开启时静默压掉整个编曲混音（有意为之但易困惑） |
| L8 | `scheduleSamples` 预热失败 | 素材解码失败静默跳过该 clip，用户无感知 |
| L9 | 引擎管道无鉴权 | 任何本地进程可连接引擎管道（审计 H9 遗留，接受：命名管道 ACL 默认同用户） |
| L10 | 更新链路 | 安装包无代码签名，sha256 与二进制同信道（R2 被攻破则两者皆可控）；`sha256_windows` 缺省时跳过校验；安装包对象无 `cache-control`（同版本重传会命中边缘缓存，靠 sha256 校验兜底报错） |
| L11 | `winres.json` 版本三处手工同步 | 本次已对齐 3.0.3.0；建议后续接入 `wails build` 前置校验脚本 |
| L12 | 前端 `_nativeVoices` 计数 | 假设 noteOn/noteOff 同后端（A10 的 panic 兜底已覆盖实际风险） |
| L13 | `download.go` `.part` 命名冲突 | 同版本并发下载由单飞保护，仅覆盖场景：上一版本安装包运行中时 Rename 失败（边缘） |
| L14 | `internal/project` 落盘 `_ =` | 已由 audit-2026-09-05.md 架构注记跟踪（数据丢失风险路径应 slog.Error） |
| L15 | dev 模式知识库路径 | exe 目录在 `build/bin` 时 Library 不在旁（仅影响开发态，打包态正常） |

## 4. 修复清单（代码索引）

| 模块 | 文件 |
|---|---|
| 更新 | `internal/update/download.go`（Total 前置 + 看门狗）、`update.go`（版本消毒）、`internal/server/handler_update.go`（Background 上下文 + UserCacheDir + 消毒） |
| 更新测试 | `internal/update/update_test.go`（+3 用例）、`internal/server/handler_update_test.go`（+2 用例） |
| 引擎 C++ | `SynthEngine.h/.cpp`（direct 变体 + ring 排空）、`EngineAudioDevice.h/.cpp`（sampleRate_ 原子化 + 越界防护 + 握手 + 降号解析统一 + 锁外析构）、`Transport.h`（回绕）、`SamplePool.h`（read 返回值 + LRU48）、`Main.cpp`（panic） |
| Go 桥 | `internal/engine/client.go`（Panic + SendMidi 判死 + markDead 容错）、`supervisor.go`（PanicAll + GetLevels TryRequest）、`internal/app/app.go`（EnginePanic）、`internal/server/handler_audio.go`（panic 路由 + 死分支清理） |
| 前端 | `engine_bridge.js`（panic）、`audio_engine.js`（原生音符定时调度 + timer 清理）、`arrange.js`（BPM 重调度）、`chat.html`（后端翻转 panic） |
| 工具 | `tools/engine_smoke/main.go`（panic 冒烟项）、`tools/bounce_probe/`（新增越界探测） |
| 杂项 | `winres/winres.json`（3.0.3.0）、`.gitignore`（.wrangler/） |

## 5. 遗留建议（优先级排序）

1. **发布验证**：下次发版前用真实 R2 清单走一遍完整「检查 → 应用内下载 →
   静默安装 → 重启」（本次修复的应用内下载路径尚无真机端到端记录）。
2. L10：更新清单给安装包对象也加 `cache-control` 头；有条件时接入代码签名。
3. L11：发版脚本校验 wails.json / winres.json / AI_MIDI.iss 三处版本一致。
4. L3/L4：SMF 读写健壮性加固（错误上浮 + 输入钳制）。
