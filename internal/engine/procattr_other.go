//go:build !windows

package engine

import "syscall"

// engineSysProcAttr 非 Windows 平台无控制台窗口问题，无需特殊属性。
func engineSysProcAttr() *syscall.SysProcAttr {
	return nil
}
