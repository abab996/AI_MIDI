package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 回归测试：禁用状态下驻留的守护循环，被唤醒时若开关仍为禁用
// （启用→禁用的快速切换竞态），旧实现在此 return 永久退出——此后
// wakeCh 再无读者，引擎直到重启应用都无法再启动。新实现必须重新
// 驻留，且后续启用仍能推进到启动流程
func TestSupervisorParkSurvivesDisabledWake(t *testing.T) {
	// exe 指向不存在的路径：启用后 runOnce 走"启动失败（文件缺失）→
	// StateFailed + parkUntilDone"分支，无需真实引擎二进制
	missing := filepath.Join(t.TempDir(), "missing-engine.exe")
	sup := NewSupervisor(Config{EnginePath: missing}, AudioSettings{Backend: "auto"})

	// 直接起 loop 而不走 Start()：跳过 cleanupOrphanEngines 对系统内
	// aimidi-engine.exe 的真实清理（测试不应杀外部进程）
	sup.mu.Lock()
	sup.ctx, sup.cancel = context.WithCancel(context.Background())
	sup.doneCh = make(chan struct{})
	sup.mu.Unlock()
	go sup.loop()
	defer sup.Stop()

	waitEngineState(t, sup, StateDisabled, 2*time.Second)

	// 唤醒但开关仍为禁用：旧实现在此退出循环（doneCh 关闭）
	select {
	case sup.wakeCh <- struct{}{}:
	default:
	}
	time.Sleep(300 * time.Millisecond)

	// 旧实现此时 loop 已死亡，后续启用永远无法生效
	sup.mu.Lock()
	sup.audio.EngineEnabled = true
	sup.mu.Unlock()
	select {
	case sup.wakeCh <- struct{}{}:
	default:
	}

	// 新实现：应推进 StateStarting → StateFailed（exe 缺失）并驻留
	waitEngineState(t, sup, StateFailed, 10*time.Second)
	sup.mu.RLock()
	parked := sup.parked
	sup.mu.RUnlock()
	if !parked {
		t.Fatal("expected parked=true after missing-exe start failure")
	}
}

func waitEngineState(t *testing.T, sup *Supervisor, want EngineState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sup.mu.RLock()
		got := sup.status.State
		sup.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("engine state %s not reached within %s", want, timeout)
}
