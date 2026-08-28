//go:build windows

package engine

import "syscall"

// engineSysProcAttr 引擎子进程属性：隐藏控制台窗口（JUCE 引擎为
// console 子系统，默认会弹出黑窗）+ CREATE_NO_WINDOW。
func engineSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
