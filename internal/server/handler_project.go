package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"aimidi/internal/chat"
	"aimidi/internal/config"
	"aimidi/internal/mcp"
	"aimidi/internal/midi"
	"aimidi/internal/project"
	"aimidi/internal/tasks"
)

func (r *Router) handleProjects(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		projects := project.ListProjects()
		if projects == nil {
			projects = []project.ProjectEntry{}
		}
		writeJSON(w, http.StatusOK, projects)

	case http.MethodPost:
		var in struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		meta, err := project.CreateProject(in.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "创建项目失败", err)
			return
		}
		// 返回与打开项目一致的完整载荷（前端 enterProject 期望 meta/ 任务/文件/草稿等字段）
		writeJSON(w, http.StatusOK, buildProjectPayload(meta))

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (r *Router) handleProjectsSearch(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query().Get("q")
	rows, ids := project.SearchProjects(q)
	if rows == nil {
		rows = [][]string{}
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": rows,
		"ids":  ids,
	})
}

func (r *Router) handleProjectsSub(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/api/projects/")
	parts := strings.Split(path, "/")
	projectID := parts[0]

	if projectID == "" {
		writeError(w, http.StatusBadRequest, "缺少项目 ID")
		return
	}

	if len(parts) == 1 {
		// /api/projects/{project_id}
		switch req.Method {
		case http.MethodGet:
			r.openProject(w, req, projectID)
		case http.MethodPut:
			var in struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
				writeError(w, http.StatusBadRequest, "项目名称不能为空")
				return
			}
			_ = project.RenameProject(projectID, in.Name)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case http.MethodDelete:
			chat.DropSession(projectID)
			tasks.TaskPurgeProject(projectID) /* 同步清除该项目的全部任务，防孤儿任务让面板永久自动弹出 */
			_ = project.DeleteProject(projectID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	sub := strings.Join(parts[1:], "/")
	switch {
	case sub == "copy" && req.Method == http.MethodPost:
		var in struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		meta, err := project.CopyProject(projectID, in.Name)
		if err != nil {
			/* err.Error() 本身是面向用户的原因（如名称冲突），保留展示并留痕日志 */
			writeErr(w, http.StatusBadRequest, err.Error(), err)
			return
		}
		writeJSON(w, http.StatusOK, meta)

	case sub == "draft":
		if req.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"text": project.LoadDraft(projectID)})
		} else if req.Method == http.MethodPost {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(req.Body).Decode(&in)
			project.SaveDraft(projectID, in.Text)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}

	case sub == "clear" && req.Method == http.MethodPost:
		if tasks.TaskHasActive(projectID) {
			writeError(w, http.StatusBadRequest, "有任务尚未完成，请等待任务结束后再操作")
			return
		}
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")
		st.FullHistory = nil
		st.ChatDisplay = nil
		st.UndoStack = nil
		st.PendingEdit = nil
		st.EditHistory = nil
		// 清空编辑历史并落盘（与 Python clear_chat 一致，否则重启后旧历史残留）
		legacy := false
		for _, t := range tasks.TaskList(projectID) {
			if t.ID == s.CurrentTaskID {
				legacy = t.Legacy
				break
			}
		}
		project.SaveEditHistory(projectID, nil, s.CurrentTaskID, legacy)
		project.SaveHistory(projectID, nil, s.MidiFiles, s.CurrentTaskID, legacy)
		writeJSON(w, http.StatusOK, map[string]any{"messages": []any{}})

	case sub == "messages/edit" && req.Method == http.MethodPost:
		if tasks.TaskHasActive(projectID) {
			writeError(w, http.StatusBadRequest, "有任务尚未完成，请等待任务结束后再操作")
			return
		}
		var in struct {
			Index int `json:"index"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")

		if in.Index < 0 || in.Index >= len(st.ChatDisplay) {
			writeError(w, http.StatusBadRequest, "消息位置无效")
			return
		}
		// 只能编辑用户消息（Python 版同样检查 role == "user"）
		if role, _ := st.ChatDisplay[in.Index]["role"].(string); role != "user" {
			writeError(w, http.StatusBadRequest, "只能编辑用户消息")
			return
		}

		histIdx, err := project.LocateUserInHistory(st.FullHistory, st.ChatDisplay, in.Index)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		origText, _ := st.ChatDisplay[in.Index]["content"].(string)
		st.PendingEdit = map[string]any{
			"index":        in.Index,
			"hist_idx":     histIdx,
			"text":         origText,
			"full_history": st.FullHistory,
			"chat_display": st.ChatDisplay,
		}
		st.FullHistory = st.FullHistory[:histIdx+1]
		st.ChatDisplay = st.ChatDisplay[:in.Index+1]

		writeJSON(w, http.StatusOK, map[string]any{
			"messages": st.ChatDisplay,
			"text":     origText,
		})

	case sub == "messages/recall" && req.Method == http.MethodPost:
		if tasks.TaskHasActive(projectID) {
			writeError(w, http.StatusBadRequest, "有任务尚未完成，请等待任务结束后再操作")
			return
		}
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")
		if st.PendingEdit != nil {
			// 撤回修改时移入回收站的文件/移除的目录，放弃撤回时一并恢复
			if rb, ok := st.PendingEdit["_rollback"].(map[string]any); ok && rb != nil {
				s.MidiFiles = project.RestoreAICreatedFiles(projectID, rb)
			}
			if fh, ok := st.PendingEdit["full_history"].([]map[string]any); ok {
				st.FullHistory = fh
			}
			if cd, ok := st.PendingEdit["chat_display"].([]map[string]any); ok {
				st.ChatDisplay = cd
			}
			st.PendingEdit = nil
			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, s.CurrentTaskID, false)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"messages": st.ChatDisplay,
			"files":    s.MidiFiles,
			"dirs":     project.ScanDirs(project.GetMidiBaseDir(projectID)),
		})

	case sub == "messages/edit-info" && req.Method == http.MethodPost:
		var in struct {
			Index int `json:"index"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")

		if in.Index < 0 || in.Index >= len(st.ChatDisplay) {
			writeJSON(w, http.StatusOK, map[string]any{
				"has_edit_history":    false,
				"has_ai_file_changes": false,
			})
			return
		}
		// 只对用户消息查询编辑信息（Python 版同样检查 role == "user"）
		if role, _ := st.ChatDisplay[in.Index]["role"].(string); role != "user" {
			writeJSON(w, http.StatusOK, map[string]any{
				"has_edit_history":    false,
				"has_ai_file_changes": false,
			})
			return
		}
		histIdx, _ := project.LocateUserInHistory(st.FullHistory, st.ChatDisplay, in.Index)
		// has_edit_history：该条消息是否有编辑历史（按 hist_idx 查找，与 Python _find_edit_history 一致）
		hasEditHistory := project.FindEditHistory(st.EditHistory, histIdx)
		entries := project.ExtractAICreatedEntries(st.FullHistory, histIdx)
		hasAIChanges := len(entries["files"]) > 0 || len(entries["dirs"]) > 0 || len(entries["deleted"]) > 0
		writeJSON(w, http.StatusOK, map[string]any{
			"has_edit_history":    hasEditHistory,
			"has_ai_file_changes": hasAIChanges,
		})

	case sub == "messages/undo-edit" && req.Method == http.MethodPost:
		if tasks.TaskHasActive(projectID) {
			writeError(w, http.StatusBadRequest, "有任务尚未完成，请等待任务结束后再操作")
			return
		}
		var in struct {
			Index int `json:"index"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")

		if in.Index < 0 || in.Index >= len(st.ChatDisplay) {
			writeError(w, http.StatusBadRequest, "消息位置无效")
			return
		}
		// 只能撤回用户消息的修改（Python 版同样检查 role == "user"）
		if role, _ := st.ChatDisplay[in.Index]["role"].(string); role != "user" {
			writeError(w, http.StatusBadRequest, "只能撤回用户消息的修改")
			return
		}

		histIdx, err := project.LocateUserInHistory(st.FullHistory, st.ChatDisplay, in.Index)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		entries := project.ExtractAICreatedEntries(st.FullHistory, histIdx)
		originalFiles := s.MidiFiles
		updatedFiles, removalInfo := project.RemoveAICreatedFiles(projectID, s.MidiFiles, entries)
		s.MidiFiles = updatedFiles
		// 记录撤回前的文件清单，放弃撤回时原样恢复（含 NoteTable 等元数据）
		removalInfo["original_files"] = originalFiles

		origText, _ := st.ChatDisplay[in.Index]["content"].(string)
		st.PendingEdit = map[string]any{
			"index":        in.Index,
			"hist_idx":     histIdx,
			"text":         origText,
			"full_history": st.FullHistory,
			"chat_display": st.ChatDisplay,
			"_rollback":    removalInfo,
		}

		st.FullHistory = st.FullHistory[:histIdx+1]
		st.ChatDisplay = st.ChatDisplay[:in.Index+1]

		resp := map[string]any{
			"messages": st.ChatDisplay,
			"text":     origText,
			"files":    s.MidiFiles,
			"dirs":     project.ScanDirs(project.GetMidiBaseDir(projectID)),
		}
		if delRels, ok := removalInfo["deleted_rels"].([]string); ok && len(delRels) > 0 {
			resp["note"] = fmt.Sprintf("AI 删除的 %d 个文件已被 AI 删除且无备份，无法恢复。", len(delRels))
		}
		writeJSON(w, http.StatusOK, resp)

	case sub == "files" && req.Method == http.MethodPost:
		r.handleProjectUploadFiles(w, req, projectID)

	case sub == "arrangement" && req.Method == http.MethodGet:
		data, err := project.ReadArrangement(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取编排数据失败")
			return
		}
		if data == nil {
			writeJSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)

	// POST 与 PUT 等价：应用退出的 sendBeacon 兜底落盘只能发 POST
	case sub == "arrangement" && (req.Method == http.MethodPut || req.Method == http.MethodPost):
		body, err := io.ReadAll(io.LimitReader(req.Body, project.ArrangementMaxBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "读取请求数据失败")
			return
		}
		if len(body) > project.ArrangementMaxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "编排数据超过大小上限")
			return
		}
		if err := project.WriteArrangement(projectID, body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case sub == "files" && req.Method == http.MethodDelete:
		r.handleProjectDeleteFiles(w, req, projectID)

	case sub == "files/move" && req.Method == http.MethodPost:
		r.handleProjectMoveFiles(w, req, projectID)

	case sub == "files/save_notes" && req.Method == http.MethodPost:
		var in struct {
			Name      string `json:"name"`
			NoteTable string `json:"note_table"`
			BPM       int    `json:"bpm"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || in.Name == "" {
			writeError(w, http.StatusBadRequest, "缺少文件名或参数")
			return
		}
		if in.BPM <= 0 {
			in.BPM = 120
		}
		baseDir := project.GetMidiBaseDir(projectID)
		targetPath, err := mcp.SafeJoin(baseDir, in.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "非法文件路径")
			return
		}
		midiBytes, err := midi.TxtToMidi(in.NoteTable, targetPath, in.BPM)
		if err != nil {
			writeError(w, http.StatusBadRequest, "生成 MIDI 失败: "+err.Error())
			return
		}
		mirrorDir := project.GetMidiMirrorDir(projectID)
		if mirrorDir != "" {
			if mPath, err := mcp.SafeJoin(mirrorDir, in.Name); err == nil {
				_ = os.MkdirAll(filepath.Dir(mPath), 0755)
				_ = os.WriteFile(mPath, midiBytes, 0644)
			}
		}
		s := chat.GetSession(projectID)
		for i := range s.MidiFiles {
			if s.MidiFiles[i].Name == in.Name {
				s.MidiFiles[i].Size = int64(len(midiBytes))
				s.MidiFiles[i].NoteTable = in.NoteTable
				break
			}
		}
		project.SaveMidiManifest(projectID, s.MidiFiles)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"files": s.MidiFiles,
		})

	case sub == "folders" && req.Method == http.MethodPost:
		var in struct {
			Name   string `json:"name"`
			Parent string `json:"parent"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		baseDir := project.GetMidiBaseDir(projectID)
		mirrorDir := project.GetMidiMirrorDir(projectID)
		rel := in.Name
		if in.Parent != "" {
			rel = filepath.Join(in.Parent, in.Name)
		}
		_, err := mcp.ExecuteTool("create_folder", map[string]any{"name": rel}, baseDir, mirrorDir)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"dirs": project.ScanDirs(baseDir),
		})

	case sub == "folders/rename" && req.Method == http.MethodPost:
		var in struct {
			Old  string `json:"old"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		oldRel := strings.TrimSpace(strings.Trim(in.Old, "/"))
		newName := strings.TrimSpace(strings.Trim(in.Name, "/"))
		if oldRel == "" {
			writeError(w, http.StatusBadRequest, "文件夹路径为空")
			return
		}
		if newName == "" || strings.Contains(newName, "/") || strings.Contains(newName, "\\") || strings.Contains(newName, "..") {
			writeError(w, http.StatusBadRequest, "文件夹名不能为空或包含路径分隔符/..")
			return
		}

		baseDir := project.GetMidiBaseDir(projectID)
		src, err := mcp.SafeJoin(baseDir, oldRel)
		if err != nil {
			writeError(w, http.StatusBadRequest, "文件夹不存在: "+oldRel)
			return
		}
		if fi, _ := os.Stat(src); fi == nil || !fi.IsDir() {
			writeError(w, http.StatusBadRequest, "文件夹不存在: "+oldRel)
			return
		}
		parentRel := filepath.ToSlash(filepath.Dir(oldRel))
		if parentRel == "." || parentRel == "" {
			parentRel = ""
		}
		var newRel string
		if parentRel != "" {
			newRel = parentRel + "/" + newName
		} else {
			newRel = newName
		}
		if newRel == oldRel {
			s := chat.GetSession(projectID)
			writeJSON(w, http.StatusOK, map[string]any{
				"files": s.MidiFiles,
				"dirs":  project.ScanDirs(baseDir),
			})
			return
		}
		dst := filepath.Join(baseDir, filepath.FromSlash(newRel))
		if _, err := os.Stat(dst); err == nil {
			writeError(w, http.StatusBadRequest, "目标已存在: "+newRel)
			return
		}

		_ = os.MkdirAll(filepath.Dir(dst), 0755)
		if err := os.Rename(src, dst); err != nil {
			writeError(w, http.StatusBadRequest, "重命名失败: "+oldRel)
			return
		}

		// 镜像同步重命名（存在才移动）
		mirrorDir := project.GetMidiMirrorDir(projectID)
		if mirrorDir != "" {
			mSrc := filepath.Join(mirrorDir, filepath.FromSlash(oldRel))
			mDst := filepath.Join(mirrorDir, filepath.FromSlash(newRel))
			if ms, err := os.Stat(mSrc); err == nil && ms.IsDir() {
				if _, err := os.Stat(mDst); os.IsNotExist(err) {
					_ = os.MkdirAll(filepath.Dir(mDst), 0755)
					_ = os.Rename(mSrc, mDst)
				}
			}
		}

		// 清单前缀替换（保留 note_table，与 Python 一致；ScanMidiFiles 会丢 note_table）
		s := chat.GetSession(projectID)
		prefix := oldRel + "/"
		for i := range s.MidiFiles {
			fname := s.MidiFiles[i].Name
			if strings.HasPrefix(fname, prefix) {
				s.MidiFiles[i].Name = newRel + fname[len(oldRel):]
				s.MidiFiles[i].Path = filepath.Join(baseDir, filepath.FromSlash(s.MidiFiles[i].Name))
			}
		}
		project.SaveMidiManifest(projectID, s.MidiFiles)

		writeJSON(w, http.StatusOK, map[string]any{
			"files": s.MidiFiles,
			"dirs":  project.ScanDirs(baseDir),
		})

	case sub == "folders/delete" && req.Method == http.MethodPost:
		var in struct {
			Folder string `json:"folder"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		folderRel := strings.TrimSpace(strings.Trim(in.Folder, "/"))
		if folderRel == "" {
			writeError(w, http.StatusBadRequest, "文件夹路径为空")
			return
		}

		baseDir := project.GetMidiBaseDir(projectID)
		mirrorDir := project.GetMidiMirrorDir(projectID)
		folder, err := mcp.SafeJoin(baseDir, folderRel)
		if err != nil {
			writeError(w, http.StatusBadRequest, "文件夹不存在: "+folderRel)
			return
		}
		if fi, _ := os.Stat(folder); fi == nil || !fi.IsDir() {
			writeError(w, http.StatusBadRequest, "文件夹不存在: "+folderRel)
			return
		}

		s := chat.GetSession(projectID)
		prefix := folderRel + "/"
		// 文件夹内文件逐个移入回收站（保留相对路径，撤销时自动重建层级）
		// 失败（被占用）时回滚已移入的文件，保持「失败时文件夹原样保留」契约
		project.ClearTrash(projectID)
		var trashRels []string
		var failed string
		failedFile := false
		for _, f := range s.MidiFiles {
			if !strings.HasPrefix(f.Name, prefix) {
				continue
			}
			rel := project.MoveToTrash(projectID, f)
			if rel == "" {
				// 文件被占用：回滚已移入回收站的文件
				project.RestoreFromTrash(projectID, trashRels)
				failed = f.Name
				failedFile = true
				break
			}
			trashRels = append(trashRels, rel)
		}
		if failedFile {
			writeError(w, http.StatusBadRequest, "无法删除被占用的文件: "+failed)
			return
		}

		// 空目录逐级删除（含子目录），撤销时 trash 恢复自动重建
		_ = project.RemoveEmptyDirs(baseDir, folderRel)
		if mirrorDir != "" {
			_ = project.RemoveEmptyDirs(mirrorDir, folderRel)
		}

		// 从清单移除已移入回收站的文件
		var kept []project.MidiFileInfo
		for _, f := range s.MidiFiles {
			if !strings.HasPrefix(f.Name, prefix) {
				kept = append(kept, f)
			}
		}
		s.MidiFiles = kept

		// 撤销栈：记录被删文件与回收站路径，撤销时恢复
		st := s.GetTaskState("")
		st.UndoStack = append(st.UndoStack, chat.UndoEntry{
			Files: kept,
			Trash: trashRels,
		})
		project.SaveMidiManifest(projectID, s.MidiFiles)

		writeJSON(w, http.StatusOK, map[string]any{
			"files": s.MidiFiles,
			"dirs":  project.ScanDirs(baseDir),
		})

	case sub == "download" && req.Method == http.MethodGet:
		r.handleProjectDownload(w, req, projectID)

	case sub == "undo" && req.Method == http.MethodPost:
		s := chat.GetSession(projectID)
		st := s.GetTaskState("")
		if len(st.UndoStack) > 0 {
			entry := st.UndoStack[len(st.UndoStack)-1]
			st.UndoStack = st.UndoStack[:len(st.UndoStack)-1]
			s.MidiFiles = entry.Files
			project.RestoreFromTrash(projectID, entry.Trash)
			project.SaveMidiManifest(projectID, s.MidiFiles)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"files": s.MidiFiles,
			"dirs":  project.ScanDirs(project.GetMidiBaseDir(projectID)),
		})

	case sub == "workspace/pick-folder" && req.Method == http.MethodPost:
		path := ""
		if r.dialogFn != nil {
			p, err := r.dialogFn()
			if err == nil {
				path = p
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path})

	case sub == "workspace/bind" && req.Method == http.MethodPost:
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Path) == "" {
			writeError(w, http.StatusBadRequest, "请输入工作区目录路径")
			return
		}
		res, err := project.BindWorkspace(projectID, in.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("绑定失败: %v", err))
			return
		}
		s := chat.GetSession(projectID)
		if mf, ok := res["midi_files"].([]project.MidiFileInfo); ok {
			s.MidiFiles = mf
		}
		// 前端 applyFiles(renderWorkspace) 依赖 path/dirs；
		// 缺失会导致绑定后路径显示空、目录树不刷新
		res["path"] = project.GetWorkspaceDir(projectID)
		res["dirs"] = project.ScanDirs(project.GetMidiBaseDir(projectID))
		writeJSON(w, http.StatusOK, res)

	case sub == "workspace/unbind" && req.Method == http.MethodPost:
		mf := project.UnbindWorkspace(projectID)
		s := chat.GetSession(projectID)
		s.MidiFiles = mf
		writeJSON(w, http.StatusOK, map[string]any{
			"midi_files": mf,
			"dirs":       project.ScanDirs(project.GetMidiBaseDir(projectID)),
		})

	case sub == "workspace/refresh" && req.Method == http.MethodPost:
		s := chat.GetSession(projectID)
		s.MidiFiles = project.SyncWorkspaceToProjects(projectID, s.MidiFiles)
		writeJSON(w, http.StatusOK, map[string]any{
			"midi_files": s.MidiFiles,
			"dirs":       project.ScanDirs(project.GetMidiBaseDir(projectID)),
		})

	case sub == "workspace/open" && req.Method == http.MethodPost:
		base := project.GetMidiBaseDir(projectID)
		// 目录不存在时先创建（否则 explorer 打不开）
		_ = os.MkdirAll(base, 0755)
		_ = exec.Command("explorer", base).Start()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": base})

	case sub == "tasks" && req.Method == http.MethodPost:
		task := tasks.TaskCreate(projectID, "新任务", false)
		s := chat.GetSession(projectID)
		s.SwitchTask(task.ID)
		writeJSON(w, http.StatusOK, map[string]any{"task": task})

	default:
		writeError(w, http.StatusNotFound, "Not Found")
	}
}

func (r *Router) openProject(w http.ResponseWriter, req *http.Request, projectID string) {
	meta, err := project.LoadProject(projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "项目不存在")
		return
	}

	taskID := req.URL.Query().Get("task_id")
	s := chat.GetSession(projectID)
	if taskID != "" {
		s.SwitchTask(taskID)
	}

	writeJSON(w, http.StatusOK, buildProjectPayload(meta))
}

// buildProjectPayload 构造项目工作台完整载荷。
// 前端（chat.js 的 applyProjectPayload/enterProject/switchTask）依赖的字段：
// meta{id,name} / current_task_id / midi_files / display_messages /
// settings{model} / workspace{bound,path} / dirs / draft / tasks。
// 注意：不要改回扁平结构（id/name/files/messages 平铺），前端不认。
func buildProjectPayload(meta project.ProjectMeta) map[string]any {
	projectID := meta.ID
	s := chat.GetSession(projectID)
	st := s.GetTaskState(s.CurrentTaskID)
	baseDir := project.GetMidiBaseDir(projectID)

	settings := config.LoadSettings()

	// 空列表序列化为 [] 而非 null（前端虽有 || [] 兜底，统一更稳）
	midiFiles := s.MidiFiles
	if midiFiles == nil {
		midiFiles = []project.MidiFileInfo{}
	}
	display := st.ChatDisplay
	if display == nil {
		display = []map[string]any{}
	}
	taskList := tasks.TaskList(projectID)
	if taskList == nil {
		taskList = []tasks.TaskRecord{}
	}
	dirs := project.ScanDirs(baseDir)
	if dirs == nil {
		dirs = []string{}
	}

	return map[string]any{
		"meta":             meta,
		"current_task_id":  s.CurrentTaskID,
		"midi_files":       midiFiles,
		"display_messages": display,
		"settings":         map[string]any{"model": settings.Model},
		"workspace":        map[string]any{"bound": meta.WorkspaceDir != "", "path": meta.WorkspaceDir},
		"dirs":             dirs,
		"draft":            project.LoadDraft(projectID),
		"tasks":            taskList,
	}
}

func (r *Router) handleProjectUploadFiles(w http.ResponseWriter, req *http.Request, projectID string) {
	err := req.ParseMultipartForm(128 << 20)
	if err != nil {
		writeError(w, http.StatusBadRequest, "解析文件失败")
		return
	}

	s := chat.GetSession(projectID)
	baseDir := project.GetMidiBaseDir(projectID)
	mirrorDir := project.GetMidiMirrorDir(projectID)
	_ = os.MkdirAll(baseDir, 0755)

	var added []project.MidiFileInfo
	formFiles := req.MultipartForm.File["files"]

	for _, fh := range formFiles {
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext != ".mid" && ext != ".midi" {
			continue
		}

		srcFile, err := fh.Open()
		if err != nil {
			continue
		}

		// 重名文件加序号（与 Python _unique_dest_path 一致），避免覆盖同名文件
		basename := filepath.Base(fh.Filename)
		dstPath := uniqueDestPath(baseDir, basename)
		_ = os.MkdirAll(filepath.Dir(dstPath), 0755)

		dstFile, err := os.Create(dstPath)
		if err != nil {
			srcFile.Close()
			continue
		}

		_, _ = io.Copy(dstFile, srcFile)
		srcFile.Close()
		dstFile.Close()

		// 校验合法 MIDI：解析失败直接删除（与 Python 一致，避免残留无效文件）。
		// 解析结果复用——此前校验与音符表各解析一次，大文件双倍耗时
		validatedNotes, verr := midi.GetNote(dstPath, false)
		if verr != nil {
			_ = os.Remove(dstPath)
			continue
		}

		// 镜像双写（绑定工作区时同步到 projects/<id>/midi）：
		// 之前错误地调用了 BindWorkspace（会反复重新绑定整个工作区），
		// 应该只是把这一个文件复制到镜像目录
		if mirrorDir != "" {
			rel, _ := filepath.Rel(baseDir, dstPath)
			mDst := filepath.Join(mirrorDir, rel)
			_ = os.MkdirAll(filepath.Dir(mDst), 0755)
			_ = project.CopyFile(dstPath, mDst)
		}

		noteList := validatedNotes
		fi, _ := os.Stat(dstPath)
		size := int64(0)
		if fi != nil {
			size = fi.Size()
		}
		rel, _ := filepath.Rel(baseDir, dstPath)

		added = append(added, project.MidiFileInfo{
			Name:      filepath.ToSlash(rel),
			Path:      dstPath,
			Size:      size,
			NoteTable: strings.Join(noteList, "\n"),
		})
	}

	// 撤销快照（与 Python _on_upload 一致：有新文件才入栈）
	if len(added) > 0 {
		st := s.GetTaskState("")
		st.UndoStack = append(st.UndoStack, chat.UndoEntry{
			Files: s.MidiFiles,
		})
	}

	for _, a := range added {
		s.MidiFiles = append(s.MidiFiles, a)
	}
	project.SaveMidiManifest(projectID, s.MidiFiles)

	writeJSON(w, http.StatusOK, map[string]any{
		"files": s.MidiFiles,
		"added": len(added),
		"dirs":  project.ScanDirs(baseDir),
	})
}

// uniqueDestPath 重名文件加序号（与 Python _unique_dest_path 一致）
func uniqueDestPath(dir, basename string) string {
	candidate := filepath.Join(dir, basename)
	counter := 2
	for {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		ext := filepath.Ext(basename)
		stem := strings.TrimSuffix(basename, ext)
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, counter, ext))
		counter++
	}
}

func (r *Router) handleProjectDeleteFiles(w http.ResponseWriter, req *http.Request, projectID string) {
	var in struct {
		Names []string `json:"names"`
	}
	_ = json.NewDecoder(req.Body).Decode(&in)

	s := chat.GetSession(projectID)
	nameSet := make(map[string]bool)
	for _, n := range in.Names {
		nameSet[n] = true
	}

	var kept []project.MidiFileInfo
	var trashRels []string

	for _, f := range s.MidiFiles {
		if nameSet[f.Name] {
			rel := project.MoveToTrash(projectID, f)
			if rel != "" {
				trashRels = append(trashRels, rel)
				continue
			}
		}
		kept = append(kept, f)
	}

	st := s.GetTaskState("")
	st.UndoStack = append(st.UndoStack, chat.UndoEntry{
		Files: s.MidiFiles,
		Trash: trashRels,
	})

	s.MidiFiles = kept
	project.SaveMidiManifest(projectID, s.MidiFiles)

	writeJSON(w, http.StatusOK, map[string]any{
		"files": s.MidiFiles,
		"dirs":  project.ScanDirs(project.GetMidiBaseDir(projectID)),
	})
}

func (r *Router) handleProjectMoveFiles(w http.ResponseWriter, req *http.Request, projectID string) {
	var in struct {
		Moves []map[string]string `json:"moves"`
	}
	_ = json.NewDecoder(req.Body).Decode(&in)

	baseDir := project.GetMidiBaseDir(projectID)
	mirrorDir := project.GetMidiMirrorDir(projectID)

	for _, m := range in.Moves {
		srcRel := m["src"]
		dstFolder := m["target"]

		srcPath, err := mcp.SafeJoin(baseDir, srcRel)
		if err != nil {
			continue
		}
		dstPath := filepath.Join(baseDir, dstFolder, filepath.Base(srcRel))
		_ = os.MkdirAll(filepath.Dir(dstPath), 0755)
		_ = os.Rename(srcPath, dstPath)

		if mirrorDir != "" {
			if mSrc, err := mcp.SafeJoin(mirrorDir, srcRel); err == nil {
				mDst := filepath.Join(mirrorDir, dstFolder, filepath.Base(srcRel))
				_ = os.MkdirAll(filepath.Dir(mDst), 0755)
				_ = os.Rename(mSrc, mDst)
			}
		}
	}

	s := chat.GetSession(projectID)
	// 重扫清单保留原音符表（此前直接 ScanMidiFiles 会把 note_table 丢光）
	s.MidiFiles = project.ScanMidiFilesKeepNotes(baseDir, s.MidiFiles)
	project.SaveMidiManifest(projectID, s.MidiFiles)

	writeJSON(w, http.StatusOK, map[string]any{
		"files": s.MidiFiles,
		"dirs":  project.ScanDirs(baseDir),
	})
}

func (r *Router) handleProjectDownload(w http.ResponseWriter, req *http.Request, projectID string) {
	namesParam := req.URL.Query().Get("names")
	s := chat.GetSession(projectID)

	var targets []project.MidiFileInfo
	if namesParam != "" {
		names := strings.Split(namesParam, ",")
		nSet := make(map[string]bool)
		for _, n := range names {
			nSet[strings.TrimSpace(n)] = true
		}
		for _, f := range s.MidiFiles {
			if nSet[f.Name] {
				targets = append(targets, f)
			}
		}
	} else {
		targets = s.MidiFiles
	}

	if len(targets) == 0 {
		writeError(w, http.StatusNotFound, "没有可下载的文件")
		return
	}

	if len(targets) == 1 {
		// 单文件直接返回
		f := targets[0]
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(f.Path)))
		http.ServeFile(w, req, f.Path)
		return
	}

	// 多文件打包 zip
	tmpZip := filepath.Join(os.TempDir(), fmt.Sprintf("AI_MIDI_%s_%s.zip", projectID, uuid.New().String()[:8]))
	defer os.Remove(tmpZip)

	zipF, err := os.Create(tmpZip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建压缩包失败")
		return
	}

	zw := zip.NewWriter(zipF)
	for _, f := range targets {
		fw, err := zw.Create(f.Name)
		if err != nil {
			continue
		}
		fileData, err := os.ReadFile(f.Path)
		if err == nil {
			_, _ = fw.Write(fileData)
		}
	}
	_ = zw.Close()
	_ = zipF.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"AI_MIDI_%s_files.zip\"", projectID))
	http.ServeFile(w, req, tmpZip)
}
