package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

// 音频引擎自测结果（audio_selftest.html 回传，内存单槽）
var (
	selftestMu   sync.Mutex
	selftestData map[string]any
)

func (r *Router) handleSelftestResult(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		var data map[string]any
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		selftestMu.Lock()
		selftestData = data
		selftestMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	selftestMu.Lock()
	data := selftestData
	selftestMu.Unlock()
	if data == nil {
		writeError(w, http.StatusNotFound, "尚无自测结果")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
