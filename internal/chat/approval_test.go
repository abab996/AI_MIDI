package chat

import (
	"testing"

	"melodymate/internal/mcp"
)

// TestApprovalGateProjectScopedInvalidate 项目隔离与工程级作废：
// 令牌编号按项目加前缀，签发返回的生效编号回传给前端与模型；
// A 项目的 mm_write_file 作废只影响 A 的在途批准，不能殃及 B。
func TestApprovalGateProjectScopedInvalidate(t *testing.T) {
	ga := ApprovalGateFor("proj-a").(projectApprovalGate)
	gb := ApprovalGateFor("proj-b").(projectApprovalGate)
	_ = mcp.ApprovalGate(ga) // 确认闸门实现 mcp.ApprovalGate（含 InvalidateProject）

	req := mcp.ApprovalRequest{Tool: "mm_build_plugin", ArgsDigest: "d"}
	idA := ga.Issue("appr-1", req)
	idB := gb.Issue("appr-1", req)
	if idA == idB {
		t.Fatalf("不同项目签发的生效编号必须不同（项目前缀隔离）: %s", idA)
	}
	if !ga.Approve(idA) || !gb.Approve(idB) {
		t.Fatalf("按生效编号批准应成功")
	}

	// A 项目内容被改 → 只作废 A 的令牌
	ga.InvalidateProject("proj-a")
	if err := ga.Consume(idA, "mm_build_plugin", "d"); err == nil {
		t.Fatalf("A 项目令牌应已被作废")
	}
	if err := gb.Consume(idB, "mm_build_plugin", "d"); err != nil {
		t.Fatalf("B 项目令牌不应受影响: %v", err)
	}

	// 项目不匹配的作废请求不应生效
	idB2 := gb.Issue("appr-2", req)
	gb.Approve(idB2)
	gb.InvalidateProject("proj-a")
	if err := gb.Consume(idB2, "mm_build_plugin", "d"); err != nil {
		t.Fatalf("项目不匹配的作废不应生效: %v", err)
	}

	// 跨项目消费被前缀校验拒绝（A 的编号拿到 B 的闸门用）
	if err := ga.Consume(idB2, "mm_build_plugin", "d"); err == nil {
		t.Fatalf("跨项目消费应被拒绝")
	}
}
