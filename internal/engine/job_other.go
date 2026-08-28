//go:build !windows

package engine

import "os"

// attachProcessToJob 非 Windows 平台的 Job Object 空实现：
// Job Object 为 Windows 内核机制；Linux 上孤儿进程由引擎侧
// 父进程看门狗兜底（引擎检测到父进程退出即自行退出）。
func attachProcessToJob(p *os.Process) {
	_ = p
}
