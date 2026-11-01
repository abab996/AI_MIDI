package mcp

// MM 编译/安装的用户确认闸门（P6 安全加固）。
//
// 背景：mm_build_plugin 会在本机执行 C++ 构建（cmake 配置阶段即可执行
// 命令），mm_install_plugin 会把原生库装进 Library/plugins。此前二者的
// 授权来自模型在工具参数里填的 confirm=true / grant=...——那是模型的
// 自我声明，不是用户的授权，间接提示词注入即可驱动本机编译。
//
// 现在的模型：
//  1. 模型调用 mm_request_build_approval 请求确认 → 签发 pending 记录，
//     并在对话里落一张批准卡（前端渲染"允许一次/拒绝"）。
//  2. 用户点击允许 → 标记该 pending 为已批准。
//  3. 模型带 approval_id 再次调用 mm_build_plugin / mm_install_plugin →
//     这里校验令牌存在、已批准、未消费、未过期、且参数摘要一致。
//
// 参数摘要绑定是关键：批准的是"这一组具体参数"，拿到批准后换一组参数
// （例如换成另一份 CMakeLists）不能复用同一令牌。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ApprovalTTL 批准令牌有效期：用户点完到模型再次调用之间的窗口。
// 过期即作废，避免陈旧批准被后续会话复用。
const ApprovalTTL = 10 * time.Minute

// ApprovalGate 批准令牌的读写接口（由对话层实现并注入 ToolEnv）。
// nil 表示没有可用的用户确认通道——此时一律拒绝需要授权的工具。
type ApprovalGate interface {
	// Consume 校验并消费一次性批准令牌。
	// 返回 nil 表示批准有效（调用方可以执行）；否则返回拒绝原因。
	Consume(approvalID, tool string, argsDigest string) error
	// InvalidateProject 作废在途批准：mm_write_file 成功写入工程内容后
	// 调用——已签发的批准绑定的是写入前的内容摘要，内容一变摘要即失效，
	// 必须重新走用户确认。projectID 为调用方所在工程，实现方按自身
	// 作用域决定作废范围。
	InvalidateProject(projectID string)
}

// ApprovalRequest 待批准的编译/安装请求
type ApprovalRequest struct {
	Tool       string   `json:"tool"`        // 将要执行的工具名
	ArgsDigest string   `json:"args_digest"` // 参数摘要（绑定"批准的就是这组参数"）
	Files      []string `json:"files"`       // 将被写入/打包的文件（给人看的清单）
	Command    string   `json:"command"`     // 将要执行的命令（给人看的摘要）
	Reason     string   `json:"reason"`      // 模型给出的理由
}

// pendingApproval 内部记录
type pendingApproval struct {
	req       ApprovalRequest
	approved  bool
	consumed  bool
	createdAt time.Time
}

// ApprovalStore 进程内的批准令牌表（按 approval_id 索引）。
// 进程重启即清空：批准不跨会话存活。
type ApprovalStore struct {
	mu   sync.Mutex
	byID map[string]*pendingApproval
	now  func() time.Time // 便于测试注入时钟
}

// NewApprovalStore 创建批准令牌表
func NewApprovalStore() *ApprovalStore {
	return &ApprovalStore{byID: make(map[string]*pendingApproval), now: time.Now}
}

// Issue 登记一条待批准请求，返回**生效的令牌编号**（对话层可能加项目
// 前缀实现隔离）。批准卡与工具回复必须携带这里返回的编号——否则
// Approve/Consume 的前缀校验永远对不上。
func (s *ApprovalStore) Issue(id string, req ApprovalRequest) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = &pendingApproval{req: req, createdAt: s.now()}
	return id
}

// Approve 用户点击"允许一次"时调用。返回 false 表示令牌不存在或已过期。
func (s *ApprovalStore) Approve(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok || p.consumed || s.now().Sub(p.createdAt) > ApprovalTTL {
		return false
	}
	p.approved = true
	return true
}

// Reject 用户点击"拒绝"时调用：删除令牌，后续带 id 的调用一律失败。
func (s *ApprovalStore) Reject(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// RejectByScope 删除所有带指定前缀的令牌（项目级作废：工程内容被修改后，
// 该项目所有在途批准的内容摘要已失效，需整体重新确认）。
func (s *ApprovalStore) RejectByScope(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byID {
		if strings.HasPrefix(id, prefix) {
			delete(s.byID, id)
		}
	}
}

// InvalidateProject 实现 ApprovalGate。原始表不区分项目（项目隔离由 chat 层
// 的令牌前缀实现，那里改用 RejectByScope 精确作废本项目），因此这里作废
// 即清空全部在途批准——对单项目通道（测试/stdio 注入）这是安全默认。
func (s *ApprovalStore) InvalidateProject(string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID = make(map[string]*pendingApproval)
}

// Request 读取待批准请求（前端渲染批准卡用）
func (s *ApprovalStore) Request(id string) (ApprovalRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return ApprovalRequest{}, false
	}
	return p.req, true
}

// Consume 实现 ApprovalGate：校验令牌可用并一次性消费。
func (s *ApprovalStore) Consume(approvalID, tool string, argsDigest string) error {
	if strings.TrimSpace(approvalID) == "" {
		return fmt.Errorf("缺少 approval_id：需要用户在界面上确认后才可执行")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[approvalID]
	if !ok {
		return fmt.Errorf("approval_id 无效或已被拒绝：请重新请求用户确认")
	}
	if p.consumed {
		return fmt.Errorf("approval_id 已被使用（一次性令牌）：请重新请求用户确认")
	}
	if s.now().Sub(p.createdAt) > ApprovalTTL {
		delete(s.byID, approvalID)
		return fmt.Errorf("approval_id 已过期（超过 %v）：请重新请求用户确认", ApprovalTTL)
	}
	if !p.approved {
		return fmt.Errorf("用户尚未确认这次操作：请先在界面上允许")
	}
	if p.req.Tool != tool {
		return fmt.Errorf("approval_id 对应的操作是 %s，与本次调用（%s）不一致", p.req.Tool, tool)
	}
	if p.req.ArgsDigest != argsDigest {
		return fmt.Errorf("参数与用户确认的内容不一致：请重新请求用户确认")
	}
	p.consumed = true
	return nil
}

// ArgsDigest 计算将要执行的操作的参数摘要。
// 只纳入会影响"执行什么"的字段，并按键名排序，保证同一组参数得到同一摘要。
// 不纳入 approval_id 本身（否则第二次调用必然摘要不同，令牌永远无法使用）。
func ArgsDigest(tool string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		if k == "approval_id" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(tool)
	for _, k := range keys {
		b.WriteString("\x00")
		b.WriteString(k)
		b.WriteString("=")
		// 用 JSON 序列化保证嵌套结构稳定且无歧义
		raw, err := json.Marshal(args[k])
		if err != nil {
			raw = []byte(fmt.Sprint(args[k]))
		}
		b.Write(raw)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
