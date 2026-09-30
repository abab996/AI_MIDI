package server

import (
	"errors"
	"testing"
)

// 卸载重试只对"文件被占用"类错误生效：其余错误（路径不存在等）必须立刻上报，
// 否则用户会白等 4 秒才看到真正的错因。
func TestLooksLikeFileBusy(t *testing.T) {
	busy := []string{
		"unlinkat D:\\x.dll: Access is denied.",
		"The process cannot access the file because it is being used by another process.",
		"sharing violation",
		"ACCESS IS DENIED",
	}
	for _, m := range busy {
		if !looksLikeFileBusy(errors.New(m)) {
			t.Fatalf("应判定为占用并重试: %s", m)
		}
	}
	plain := []string{
		"no such file or directory",
		"directory not empty",
		"permission denied by policy", // 含 deny 但不是 Windows 占用语义
	}
	for _, m := range plain {
		if looksLikeFileBusy(errors.New(m)) {
			t.Fatalf("不该判定为占用（会白等重试）: %s", m)
		}
	}
	if looksLikeFileBusy(nil) {
		t.Fatal("nil 不该判定为占用")
	}
}
