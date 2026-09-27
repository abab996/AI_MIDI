package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"melodymate/internal/project"
)

// handleArrangementOps POST /api/projects/{id}/arrangement/ops
//
// 编排数据的定向修改入口，AI 工具（internal/mcp/arrange_tools.go）与前端
// 机架/音源操作共用同一实现——写锁、校验、落盘只有这一份。
//
// 请求：{"ops":[{"op":"setTrackSource","track":0,"itemId":"rk_xxx","presetId":"0:48"}]}
// 响应：{"ok":true,"rev":7,"summary":["..."],"changed":["rack","tracks"],
//
//	"arrangement":{...完整 v2 数据...}}
//
// 返回完整数据是刻意的：调用方改完就能直接热应用，不必再发一次 GET，
// 也避免"改完读到的却是别人写的新版本"这类竞态窗口。
func (r *Router) handleArrangementOps(w http.ResponseWriter, req *http.Request, projectID string) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, project.ArrangementMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求数据失败")
		return
	}
	if len(body) > project.ArrangementMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "请求数据超过大小上限")
		return
	}

	var in struct {
		Ops    []map[string]any `json:"ops"`
		Op     map[string]any   `json:"op"`     // 单操作简写（{"op":{...}}）
		Client string           `json:"client"` // "ai" / "ui"，仅用于日志与回显
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "非法的 JSON 数据")
		return
	}
	ops := in.Ops
	if len(ops) == 0 && len(in.Op) > 0 {
		ops = []map[string]any{in.Op}
	}
	if len(ops) == 0 {
		writeError(w, http.StatusBadRequest, "缺少 ops")
		return
	}

	res, err := project.ApplyOps(projectID, ops)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "读取编排数据失败") {
			status = http.StatusInternalServerError
		}
		writeErr(w, status, err.Error(), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"rev":         res.Rev,
		"summary":     res.Summary,
		"changed":     res.Changed,
		"arrangement": res.Arrangement,
	})
}
