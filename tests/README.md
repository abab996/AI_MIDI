# E2E 测试说明

本目录包含为“全部默认 JUCE”与“尾音不截断”专项新增的端到端验证。

## 测试矩阵

| 用例 | 文件 | 验证点 |
|---|---|---|
| 配置默认 | `internal/config/audio_settings_test.go` | 默认 `backend=auto`，持久化，非法回退 |
| 多轨隔离 | `internal/engine/audio_e2e_test.go` | 32轨 `SendMidiTrack` 钳制，`Bounce` 超时，`lastSoundFonts` 隔离 |
| 尾音不截 | `internal/midi/tail_test.go` | `beats*spb+tail*sr`，SMF 本身不含尾但音频需含 |
| 管线端到端 | `tests/audio_pipeline_e2e_test.go` | `SMF`→`bounce` 尾音完整 |
| 前端路由 | `tests/frontend_e2e_test.go` | `isNativePreferred`、`noteOnTrack`、`timecode/levels`、`统一按钮`、`M4热插拔/延迟` |
| Splash/隐藏窗口 | `internal/app/splash_test.go` | 去掉 1400ms/400ms 假等待，跟随引擎 |
| 前端静态 | `internal/server/frontend_static_test.go` | `audio_engine`/`pianoroll`/`bridge` 是否切 JUCE |
| API | `internal/server/audio_api_test.go` | `backend` 切换、`bounce 503`、`路径穿越`、`按钮统一` |
| 运行器 | `tests/run_e2e.go` | 启动临时 `httptest` 服务器，测 5 项 API |

## 运行

```powershell
# 单元 + 集成
go test ./... -count=1

# 仅 E2E（需 -tags e2e 的留空，此处直接跑）
go run tests/run_e2e.go

# 完整（含前端静态）
go test ./internal/app -run TestSplash -count=1
go test ./internal/server -run TestFrontend -count=1
```

## 覆盖

- 默认 `JUCE`：`engine_bridge` → `audio_engine`/`pianoroll`/`SamplePool` 全链路
- 尾音：`SMF` 不含尾但 `bounce` 含 `tailSec*sr`
- 设置：`AUTO`/`WEBAUDIO` 切换持久化
- 安全：`bounce/file` 路径穿越拦截
- UI：`settings.html` ASIO 按钮已统一，无深底硬编码
- 启动：`main.go` 跟随 `engineSup.Status()`，`supervisor.go` `HideWindow`
