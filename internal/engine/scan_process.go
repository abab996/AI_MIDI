package engine

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// errScanTimeout 扫描子进程超时（调用方据此给出可解释错误）。
var errScanTimeout = errors.New("扫描超时")

// runScanProcess 拉起一次扫描子进程并收集输出。超时会强制终止子进程；
// 崩溃（非零退出）原样返回错误，供 ScanPlugins 区分处理。
func runScanProcess(exe string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = engineSysProcAttr()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return output.String(), errScanTimeout
	}
	return output.String(), err
}
