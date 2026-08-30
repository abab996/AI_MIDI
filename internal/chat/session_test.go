package chat

import (
	"context"
	"testing"
	"time"
)

// TestAcquireProjectChatLockCancel 等锁必须响应 ctx 取消：上游卡死持锁时，
// 同项目后续消息的等锁不能无限阻塞（否则界面一直转圈）。
func TestAcquireProjectChatLockCancel(t *testing.T) {
	pid := "lock-cancel-test"
	// 锁空闲：首次获取立即成功
	if err := AcquireProjectChatLock(context.Background(), pid); err != nil {
		t.Fatalf("首次获取项目锁失败: %v", err)
	}

	// 锁被持有（未释放）：二次获取应阻塞，直到 ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- AcquireProjectChatLock(ctx, pid)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("期望 ctx 取消后获取失败，实际拿到了锁")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等锁未响应 ctx 取消（卡死）")
	}

	// 释放后再次获取：成功（锁可复用，不因取消而失效）
	ReleaseProjectChatLock(pid)
	if err := AcquireProjectChatLock(context.Background(), pid); err != nil {
		t.Fatalf("释放后重新获取失败: %v", err)
	}
	ReleaseProjectChatLock(pid)
}
