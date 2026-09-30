package chat

// 用户确认闸门的对话层绑定（P6 安全加固）。
//
// mcp 包只定义"需要确认"的抽象（ApprovalGate），确认状态的归属在这里：
// 批准按项目隔离，令牌由对话流签发、由前端点按允许/拒绝，工具执行时消费。
// 这样 mm_build_plugin 的授权来自真实用户操作，而不是模型填的 confirm。

import (
	"fmt"
	"strings"

	"melodymate/internal/mcp"
)

// approvalStore 进程内的批准令牌表（线程安全，按 approval_id 索引）
var approvalStore = mcp.NewApprovalStore()

// projectApprovalGate 把批准表按项目隔离：令牌编号里带项目前缀，
// 消费时校验前缀一致，避免跨项目复用（多项目并行时尤其重要）。
type projectApprovalGate struct {
	projectID string
	store     *mcp.ApprovalStore
}

// Issue 登记待批准请求（编号加上项目前缀）
func (g projectApprovalGate) Issue(id string, req mcp.ApprovalRequest) {
	g.store.Issue(g.scoped(id), req)
}

// Consume 校验并消费一次性批准令牌（校验项目前缀）
func (g projectApprovalGate) Consume(approvalID, tool string, argsDigest string) error {
	if !strings.HasPrefix(approvalID, g.scope()) {
		return fmt.Errorf("approval_id 不属于当前项目")
	}
	return g.store.Consume(approvalID, tool, argsDigest)
}

// Approve 用户在界面上点击"允许一次"
func (g projectApprovalGate) Approve(approvalID string) bool {
	if !strings.HasPrefix(approvalID, g.scope()) {
		return false
	}
	return g.store.Approve(approvalID)
}

// Reject 用户在界面上点击"拒绝"
func (g projectApprovalGate) Reject(approvalID string) {
	if strings.HasPrefix(approvalID, g.scope()) {
		g.store.Reject(approvalID)
	}
}

// Request 读取待批准请求（前端渲染批准卡用）
func (g projectApprovalGate) Request(approvalID string) (mcp.ApprovalRequest, bool) {
	if !strings.HasPrefix(approvalID, g.scope()) {
		return mcp.ApprovalRequest{}, false
	}
	return g.store.Request(approvalID)
}

func (g projectApprovalGate) scope() string {
	return "p" + strings.ReplaceAll(g.projectID, "|", "") + "|"
}

func (g projectApprovalGate) scoped(id string) string {
	return g.scope() + id
}

// ApprovalGateFor 返回某项目的批准闸门（注入 mcp.ToolEnv 用）
func ApprovalGateFor(projectID string) mcp.ApprovalGate {
	return projectApprovalGate{projectID: projectID, store: approvalStore}
}

// ApproveBuildApproval 处理"允许一次"：返回 false 表示令牌不存在/已过期。
// 由 HTTP/Wails 入口调用。
func ApproveBuildApproval(projectID, approvalID string) bool {
	return ApprovalGateFor(projectID).(projectApprovalGate).Approve(approvalID)
}

// RejectBuildApproval 处理"拒绝"：删除令牌，模型后续带同一 id 会被拒。
func RejectBuildApproval(projectID, approvalID string) {
	ApprovalGateFor(projectID).(projectApprovalGate).Reject(approvalID)
}
