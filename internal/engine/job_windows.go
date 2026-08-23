//go:build windows

package engine

import (
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 引擎进程绑定 Job Object（KILL_ON_JOB_CLOSE）：
// 主程序无论正常退出、崩溃还是被强杀，内核都会随 Job 句柄关闭收割引擎，
// 杜绝孤儿 aimidi-engine.exe 占住声卡。正常路径仍先走协议层优雅退出，
// Job 收割只是兜底。

var (
	jobOnce   sync.Once
	jobHandle windows.Handle
)

func ensureEngineJob() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil || h == 0 {
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
		return
	}
	defer windows.CloseHandle(ph)
	_ = windows.AssignProcessToJobObject(j, ph)
}
