//go:build !windows

package app

import "time"

// SplashController 非 Windows 平台的启动图空控制器：
// Win32 层透明启动图为 Windows 专属，Linux/macOS 直接跳过——
// Wait 立即返回让主窗口即刻显示，Close 为空操作。
type SplashController struct{}

// ShowNativeTransparentSplash 非 Windows 平台不弹启动图。
func ShowNativeTransparentSplash(duration time.Duration) *SplashController {
	_ = duration
	return &SplashController{}
}

// Close 空实现（无启动图可关）。
func (c *SplashController) Close() {}

// Wait 立即返回（无启动图等待）。
func (c *SplashController) Wait() {}
