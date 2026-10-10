package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"melodymate/internal/project"
)

func TestArrangementRejectsStaleAndMissingRevision(t *testing.T) {
	_, pid := withTempProjectRoot(t)
	r := NewRouter(nil, nil)
	put := func(data []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "http://127.0.0.1/api/projects/"+pid+"/arrangement", bytes.NewReader(data))
		req.Header.Set("X-MM-Token", r.AuthToken())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	initial, _ := json.Marshal(project.NewArrangement())
	if w := put(initial); w.Code != 200 {
		t.Fatalf("initial save: %d %s", w.Code, w.Body.String())
	}
	stale, _ := project.ReadArrangement(pid)
	_, err := project.ApplyOps(pid, []map[string]any{{"op": "setMaster", "gain": 0.2}})
	if err != nil {
		t.Fatal(err)
	}
	w := put(stale)
	var reply map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if w.Code != 409 || reply["code"] != "arrangement_conflict" || reply["rev"] != float64(2) {
		t.Fatalf("stale save must return latest snapshot: %d %s", w.Code, w.Body.String())
	}
	latest := project.LoadArrangementV2(pid)
	if latest["mixer"].(map[string]any)["master"].(map[string]any)["gain"] != 0.2 {
		t.Fatal("AI edit was overwritten")
	}
	if w = put([]byte(`{"version":2,"tracks":[]}`)); w.Code != 400 {
		t.Fatalf("missing rev: %d", w.Code)
	}
}

func TestArrangementHTTPSizeBoundary(t *testing.T) {
	_, pid := withTempProjectRoot(t)
	r := NewRouter(nil, nil)
	for _, size := range []int{2 << 20, project.ArrangementMaxBytes, project.ArrangementMaxBytes + 1} {
		prefix := `{"version":2,"rev":0,"padding":"`
		data := []byte(prefix + strings.Repeat("x", size-len(prefix)-2) + `"}`)
		req := httptest.NewRequest("PUT", "http://127.0.0.1/api/projects/"+pid+"/arrangement", bytes.NewReader(data))
		req.Header.Set("X-MM-Token", r.AuthToken())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if size <= project.ArrangementMaxBytes && w.Code != 200 {
			t.Fatalf("%d bytes: %d %s", size, w.Code, w.Body.String())
		}
		if size > project.ArrangementMaxBytes && w.Code != 413 {
			t.Fatalf("oversize: %d", w.Code)
		}
		// Independent boundary requests all start from revision zero.
		if size <= project.ArrangementMaxBytes {
			file, _ := project.ArrangementFile(pid)
			_ = os.Remove(file)
		}
	}
}
