package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"melodymate/internal/chat"
	"melodymate/internal/config"
	"melodymate/internal/midi"
	"melodymate/internal/project"
	"melodymate/internal/server"
	"melodymate/internal/tasks"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func observe(name string, condition bool, detail string) {
	if !condition {
		panic("observation changed: " + name + ": " + detail)
	}
	fmt.Printf("CONFIRMED %s: %s\n", name, detail)
}
func write(path string, data []byte) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, data, 0600))
}
func read(path string) []byte { b, err := os.ReadFile(path); must(err); return b }
func create(name string) project.ProjectMeta {
	p, err := project.CreateProject(name)
	must(err)
	return p
}
func main() {
	fixtureRoot, err := os.MkdirTemp("", "mm-audit-20261004-")
	must(err)
	defer func() {
		rel, err := filepath.Rel(os.TempDir(), fixtureRoot)
		if err == nil && !strings.HasPrefix(rel, "..") && strings.HasPrefix(filepath.Base(fixtureRoot), "mm-audit-20261004-") {
			_ = os.RemoveAll(fixtureRoot)
		}
	}()
	config.ProjectRoot = fixtureRoot
	config.ProjectsDir = filepath.Join(fixtureRoot, "projects")
	config.LibraryDir = filepath.Join(fixtureRoot, "Library")
	config.LibraryUserDir = filepath.Join(config.LibraryDir, "user")
	config.SettingsFile = filepath.Join(fixtureRoot, "settings.json")
	config.OutputDir = filepath.Join(fixtureRoot, "output")
	project.ResetProjectsIndexCache()
	router := server.NewRouter(nil, nil)
	call := func(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(body))
		req.Header.Set("X-MM-Token", router.AuthToken())
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	jsonCall := func(method, path string, body any) *httptest.ResponseRecorder {
		b, err := json.Marshal(body)
		must(err)
		return call(method, path, b, "application/json")
	}
	if len(os.Args) > 1 && os.Args[1] == "--task-switch-race" {
		p := create("concurrent-task-switch")
		a, b := tasks.TaskCreate(p.ID, "A", false), tasks.TaskCreate(p.ID, "B", false)
		var wg sync.WaitGroup
		for _, id := range []string{a.ID, b.ID} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					mustHTTP(jsonCall("GET", "/api/projects/"+p.ID+"?task_id="+id, nil), 200)
				}
			}(id)
		}
		wg.Wait()
		fmt.Println("Concurrent real task-switch handlers completed; inspect race detector output.")
		return
	}
	note := `[note: "C4", velocity: "90", start: "0", end: "1"]`
	data, err := midi.BuildSMF([]midi.NoteEvent{{Note: "C4", Velocity: 90, Start: 0, End: 1}}, 150)
	must(err)
	otherData, err := midi.BuildSMF([]midi.NoteEvent{{Note: "D4", Velocity: 90, Start: 0, End: 1}}, 150)
	must(err)

	// An equal-size external MIDI edit is skipped, and the old note table survives.
	p := create("mirror")
	ws := filepath.Join(fixtureRoot, "external")
	write(filepath.Join(ws, "song.mid"), data)
	project.SetWorkspaceDir(p.ID, ws)
	project.SyncWorkspaceToProjects(p.ID, nil)
	previous := []project.MidiFileInfo{{Name: "song.mid", Path: filepath.Join(ws, "song.mid"), Size: int64(len(data)), NoteTable: note}}
	write(filepath.Join(ws, "song.mid"), otherData)
	result := project.SyncWorkspaceToProjects(p.ID, previous)
	observe("WORKSPACE-SAME-SIZE", len(data) == len(otherData) && bytes.Equal(read(filepath.Join(project.MidiDir(p.ID), "song.mid")), data) && result[0].NoteTable == note,
		"external D4 edit leaves C4 in mirror and note_table")

	// A previously bound directory replaced by a regular file must not mean empty workspace.
	p = create("unreadable-root")
	ws = filepath.Join(fixtureRoot, "replaced-workspace")
	must(os.MkdirAll(ws, 0755))
	write(filepath.Join(project.MidiDir(p.ID), "safe.mid"), data)
	project.SetWorkspaceDir(p.ID, ws)
	must(os.Remove(ws))
	write(ws, []byte("directory replaced by a file"))
	result = project.SyncWorkspaceToProjects(p.ID, []project.MidiFileInfo{{Name: "safe.mid", Path: filepath.Join(project.MidiDir(p.ID), "safe.mid")}})
	_, statErr := os.Stat(filepath.Join(project.MidiDir(p.ID), "safe.mid"))
	observe("WORKSPACE-INCOMPLETE-SCAN", os.IsNotExist(statErr) && len(result) == 0, "invalid workspace root deletes mirror and clears manifest")

	// Binding to the managed MIDI directory copies each file back into that same directory.
	p = create("self-binding")
	write(filepath.Join(project.MidiDir(p.ID), "song.mid"), data)
	_, err = project.BindWorkspace(p.ID, project.MidiDir(p.ID))
	must(err)
	_, statErr = os.Stat(filepath.Join(project.MidiDir(p.ID), "song_2.mid"))
	observe("WORKSPACE-SELF-BIND", statErr == nil, "self-binding is accepted and duplicates song.mid as song_2.mid")

	// Copied nonlegacy histories have no corresponding destination task registry entries.
	p = create("multi-task-source")
	task := tasks.TaskCreate(p.ID, "separate task", false)
	project.SaveHistory(p.ID, []map[string]any{{"role": "user", "content": "copied task message"}}, nil, task.ID, false)
	copyMeta, err := project.CopyProject(p.ID, "multi-task-copy")
	must(err)
	copyDir, err := project.ProjectDir(copyMeta.ID)
	must(err)
	_, statErr = os.Stat(filepath.Join(copyDir, "tasks", task.ID, "history.json"))
	_ = chat.GetSession(copyMeta.ID)
	listed := tasks.TaskList(copyMeta.ID)
	observe("COPY-TASK-ORPHANS", statErr == nil && len(listed) == 1 && listed[0].Legacy, "copied task history exists on disk but destination lists only an empty legacy task")

	// Selecting a task in another client changes the target of a task-less edit API.
	p = create("shared-task-selection")
	taskA := tasks.TaskCreate(p.ID, "A", false)
	taskB := tasks.TaskCreate(p.ID, "B", false)
	project.SaveHistory(p.ID, []map[string]any{{"role": "user", "content": "message-A"}}, nil, taskA.ID, false)
	project.SaveHistory(p.ID, []map[string]any{{"role": "user", "content": "message-B"}}, nil, taskB.ID, false)
	recA := jsonCall("GET", "/api/projects/"+p.ID+"?task_id="+taskA.ID, nil)
	mustHTTP(recA, 200)
	recB := jsonCall("GET", "/api/projects/"+p.ID+"?task_id="+taskB.ID, nil)
	mustHTTP(recB, 200)
	edit := jsonCall("POST", "/api/projects/"+p.ID+"/messages/edit", map[string]any{"index": 0})
	mustHTTP(edit, 200)
	var edited map[string]any
	must(json.Unmarshal(edit.Body.Bytes(), &edited))
	observe("TASK-SELECTION-SHARED", edited["text"] == "message-B", "client A edits its message after client B selects B: API edits B because no task_id is sent")

	// Unknown task IDs become CurrentTaskID and later reach HistoryFile without validation.
	p = create("task-id-path")
	target := filepath.Join(config.ProjectsDir, "task-id-escape", "history.json")
	badSelection := jsonCall("GET", "/api/projects/"+p.ID+"?task_id=../../task-id-escape", nil)
	mustHTTP(badSelection, 200)
	cleared := jsonCall("POST", "/api/projects/"+p.ID+"/clear", map[string]any{})
	mustHTTP(cleared, 200)
	_, escapedErr := os.Stat(target)
	observe("TASK-ID-PATH-ESCAPE", escapedErr == nil, "unregistered ../ task_id is accepted, clear writes history.json outside the selected project")

	// Distinct deletions of the same source name lose the original-name mapping.
	p = create("trash-names")
	song := filepath.Join(project.MidiDir(p.ID), "song.mid")
	write(song, data)
	first := project.MoveToTrash(p.ID, project.MidiFileInfo{Name: "song.mid", Path: song})
	write(song, otherData)
	second := project.MoveToTrash(p.ID, project.MidiFileInfo{Name: "song.mid", Path: song})
	project.RestoreFromTrash(p.ID, []string{second})
	_, originalErr := os.Stat(song)
	_, renamedErr := os.Stat(filepath.Join(project.MidiDir(p.ID), second))
	observe("TRASH-ORIGINAL-NAME", first != second && os.IsNotExist(originalErr) && renamedErr == nil, "second song.mid deletion is restored under its trash collision filename, not original name")

	// Malformed draft JSON overwrites a valid draft with empty text.
	p = create("draft")
	project.SaveDraft(p.ID, "unsent important text")
	rec := call("POST", "/api/projects/"+p.ID+"/draft", []byte("{invalid"), "application/json")
	observe("DRAFT-MALFORMED-CLEAR", rec.Code == 200 && project.LoadDraft(p.ID) == "", "malformed JSON returns 200 and clears an existing draft")

	// Simulated disk write failure still reports a successful draft save.
	projectDir, err := project.ProjectDir(p.ID)
	must(err)
	draftPath := filepath.Join(projectDir, "draft.txt")
	must(os.Remove(draftPath))
	must(os.Mkdir(draftPath, 0755))
	rec = jsonCall("POST", "/api/projects/"+p.ID+"/draft", map[string]any{"text": "new text"})
	observe("DRAFT-FALSE-SUCCESS", rec.Code == 200 && project.LoadDraft(p.ID) == "", "draft.txt is a directory: write fails, HTTP still returns success")

	// New projects also claim success when the configured project root cannot be a directory.
	originalProjectsDir := config.ProjectsDir
	blockedRoot := filepath.Join(fixtureRoot, "blocked-projects")
	write(blockedRoot, []byte("file"))
	config.ProjectsDir = blockedRoot
	project.ResetProjectsIndexCache()
	broken, createErr := project.CreateProject("cannot persist")
	_, statErr = os.Stat(filepath.Join(blockedRoot, broken.ID, "meta.json"))
	observe("PROJECT-FALSE-SUCCESS", createErr == nil && statErr != nil, "CreateProject returns metadata and nil error without a persisted project")
	config.ProjectsDir = originalProjectsDir
	project.ResetProjectsIndexCache()

	// Deleting the final piano-roll note cannot save an empty, valid MIDI track.
	p = create("empty-notes")
	song = filepath.Join(project.MidiDir(p.ID), "song.mid")
	write(song, data)
	rec = jsonCall("POST", "/api/projects/"+p.ID+"/files/save_notes", map[string]any{"name": "song.mid", "note_table": "", "bpm": 150})
	observe("EMPTY-NOTES-NOT-SAVED", rec.Code == 400 && bytes.Equal(read(song), data), "empty note table returns 400, original note remains on disk")

	// Undoing an upload restores only the in-memory list, leaving the uploaded file on disk.
	p = create("upload-undo")
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	part, err := form.CreateFormFile("files", "song.mid")
	must(err)
	_, err = part.Write(data)
	must(err)
	must(form.Close())
	rec = call("POST", "/api/projects/"+p.ID+"/files", upload.Bytes(), form.FormDataContentType())
	mustHTTP(rec, 200)
	rec = jsonCall("POST", "/api/projects/"+p.ID+"/undo", map[string]any{})
	mustHTTP(rec, 200)
	_, statErr = os.Stat(filepath.Join(project.MidiDir(p.ID), "song.mid"))
	observe("UPLOAD-UNDO-DISK", len(chat.GetSession(p.ID).MidiFiles) == 0 && statErr == nil, "undo hides the upload from manifest but leaves song.mid on disk")

	// Deadlines cannot traverse the router's capturingWriter wrapper.
	w := &deadlineWriter{header: make(http.Header)}
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/chat", strings.NewReader(`{"project_id":"deadline","message":"hello"}`))
	req.Header.Set("X-MM-Token", router.AuthToken())
	router.ServeHTTP(w, req)
	observe("SSE-DEADLINE-WRAPPER", w.deadlines == 0 && bytes.Contains(w.body.Bytes(), []byte(`"type":"error"`)), "SSE callback executes but the underlying SetWriteDeadline is never invoked")

	rec = jsonCall("GET", "/api/audio/settings", nil)
	var audio map[string]any
	must(json.Unmarshal(rec.Body.Bytes(), &audio))
	observe("E2E-OBSOLETE-BACKEND", rec.Code == 200 && audio["backend"] == nil && audio["engine_enabled"] == true, "runner expects backend:auto, real API returns engine_enabled:true")
}
func mustHTTP(rec *httptest.ResponseRecorder, want int) {
	if rec.Code != want {
		panic(fmt.Sprintf("HTTP %d: %s", rec.Code, rec.Body.String()))
	}
}

type deadlineWriter struct {
	header    http.Header
	body      bytes.Buffer
	deadlines int
}

func (w *deadlineWriter) Header() http.Header              { return w.header }
func (w *deadlineWriter) WriteHeader(int)                  {}
func (w *deadlineWriter) Write(p []byte) (int, error)      { return w.body.Write(p) }
func (w *deadlineWriter) Flush()                           {}
func (w *deadlineWriter) SetWriteDeadline(time.Time) error { w.deadlines++; return nil }
