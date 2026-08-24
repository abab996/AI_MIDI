//go:build windows

package engine

import (
	"log/slog"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 引擎进程绑定 Job Object（KILL_ON_JOB_CLOSE）：
// 主程序无论正常退出、崩溃还是被强杀，内核都会随 Job 句柄关闭收割引擎，
// 杜绝孤儿 aimidi-engine.exe 占住声卡。正常路径仍先走协议层优雅退出，
// Job 收割只是兜底。挂靠失败不致命（引擎侧另有父进程看门狗兜底），
// 但必须可观测——此前全程静默，Job 失效无人知晓（孤儿引擎现场即证）。

var (
	jobOnce   sync.Once
	jobHandle windows.Handle
)

func ensureEngineJob() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil || h == 0 {
			slog.Warn("[engine] Job Object 创建失败，孤儿兜底降级为父进程看门狗", "err", err.Error())
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			slog.Warn("[engine] Job Object 配置 KILL_ON_JOB_CLOSE 失败，孤儿兜底降级为父进程看门狗", "err", err.Error())
			_ = windows.CloseHandle(h)
			return
		}
		jobHandle = h
	})
	return jobHandle
}

// attachProcessToJob 把引擎进程挂入主程序的 Job
func attachProcessToJob(p *os.Process) {
	j := ensureEngineJob()
	if j == 0 {
		return
	}
	const desired = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	ph, err := windows.OpenProcess(desired, false, uint32(p.Pid))
	if err != nil {
		slog.Warn("[engine] 挂靠 Job 打开引擎进程失败（孤儿兜底降级为父进程看门狗）", "pid", p.Pid, "err", err.Error())
		return
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(j, ph); err != nil {
		// 常见诱因：主程序嵌套于其它 Job（Windows Terminal/IDE 启动）且被拒
		slog.Warn("[engine] 引擎挂靠 Job 失败（孤儿兜底降级为父进程看门狗）", "pid", p.Pid, "err", err.Error())
	}
}
