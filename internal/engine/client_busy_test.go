package engine

import "testing"

// P-M6：扫描期间请求排队超时不应把健康会话判死并触发引擎重启。
func TestBusyTimeoutDoesNotKillSession(t *testing.T) {
	c := &Client{dead: make(chan struct{})}
	busy := true
	c.SetBusyCheck(func() bool { return busy })

	c.markDeadIfIdle()
	select {
	case <-c.dead:
		t.Fatal("busy timeout must not close the session")
	default:
	}

	busy = false
	c.markDeadIfIdle()
	select {
	case <-c.dead:
	default:
		t.Fatal("idle timeout must close the session")
	}
}
