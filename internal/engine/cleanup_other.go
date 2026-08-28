//go:build !windows

package engine

import "log/slog"

// cleanupOrphanEngines 非 Windows 平台的启动清场空实现：
// 孤儿收割依赖 Windows 进程快照枚举，Linux 上由引擎侧的
// 父进程看门狗（--parent PID）自行退出兜底。
func cleanupOrphanEngines(skipPID int) {
	_ = skipPID
	slog.Debug("[engine] 非 Windows 平台跳过孤儿清场（依赖引擎父进程看门狗）")
}
