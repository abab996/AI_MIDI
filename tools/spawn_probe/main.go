// 模拟 supervisor 的最小拉起器：启动引擎、打印 pid、保持存活
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	bin := os.Args[1]
	_ = len(os.Args)
	cmd := exec.Command(bin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("SPAWN-FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PID:", cmd.Process.Pid)
	time.Sleep(20 * time.Second)
	_ = cmd.Process.Kill()
}
