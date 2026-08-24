//go:build windows

package engine

import (
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cleanupOrphanEngines 启动清场：枚举系统全部进程，终止镜像名为
// aimidi-engine.exe 的残留实例（skipPID 保留，通常为本会话当前子进程）。
//
// 背景：主程序被强杀时 defer Stop 不执行，Job Object 挂靠若失败
// （嵌套 Job 限制等），引擎会成为孤儿——占住声卡设备，且下次启动
// 时与新引擎并存（用户看到的"一次启动两个引擎"即历史孤儿叠加）。
// 引擎为主程序专属附属进程，无手动长开场景，清场语义安全。
func cleanupOrphanEngines(skipPID int) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		slog.Warn("[engine] 进程快照创建失败，跳过残留引擎清场", "err", err.Error())
		return
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return
	}

	killed := 0
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if name == "aimidi-engine.exe" && int(entry.ProcessID) != skipPID {
			if terminateByPID(entry.ProcessID) {
				killed++
				slog.Info("[engine] 清理残留引擎进程", "pid", entry.ProcessID)
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	_ = killed
}

func terminateByPID(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		// 进程已退出或无权限（如残留自旧会话）：无需处理
		return false
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return false
	}
	return true
}
