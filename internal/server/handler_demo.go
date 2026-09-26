package server

import (
	_ "embed"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"aimidi/internal/midi"
	"aimidi/internal/project"
)

// demoMIDI 内置示例 MIDI（小星星第一乐句 + 和声层，tools/dev/gen-demo-midi
// 生成）：首次使用没有素材，空档案库一键载入示例工程即可试听/编辑
//
//go:embed assets/demo.mid
var demoMIDI []byte

const demoProjectName = "示例工程 · 旋律与和弦"

// handleDemoProject POST /api/demo/project：创建内置示例工程并返回与
// 打开项目一致的完整载荷。示例工程可当普通项目编辑/删除
func (r *Router) handleDemoProject(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	meta, err := project.CreateProject(demoProjectName)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "创建示例工程失败", err)
		return
	}
	pid := meta.ID

	baseDir := project.GetMidiBaseDir(pid)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建示例工程目录失败", err)
		return
	}
	demoPath := filepath.Join(baseDir, "示例旋律-小星星.mid")
	if err := os.WriteFile(demoPath, demoMIDI, 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入示例文件失败", err)
		return
	}

	noteList, _ := midi.GetNote(demoPath, false)
	files := []project.MidiFileInfo{{
		Name:      "示例旋律-小星星.mid",
		Path:      demoPath,
		Size:      int64(len(demoMIDI)),
		NoteTable: strings.Join(noteList, "\n"),
	}}
	project.SaveMidiManifest(pid, files)
	// 示例素材按 100 BPM 制作，同步项目全局 BPM 让卷帘/编曲窗口径一致
	project.SaveProjectBPM(pid, 100)

	slog.Info("示例工程已创建", "id", pid)
	writeJSON(w, http.StatusOK, buildProjectPayload(meta))
}
