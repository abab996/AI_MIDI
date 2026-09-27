package server

import (
	"net/http"

	"melodymate/internal/app"
	"melodymate/internal/config"
)

// handleMigration GET /api/migration：首启返回从 AI_MIDI 复制过来的文件数，并只提示一次。
func (r *Router) handleMigration(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	result, ok := app.TakeMigrationAnnouncement(config.ProjectRoot)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":    true,
		"from":       result.From,
		"files":      result.Files,
		"soundfonts": result.SoundFonts,
	})
}
