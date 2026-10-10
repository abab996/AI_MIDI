package mcp

import (
	"melodymate/internal/project"
	"melodymate/internal/soundlib"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIPluginAssignmentUsesRegisteredVersionAndStableTrack(t *testing.T) {
	root, pid := withArrangeEnv(t)
	var sources []soundlib.Source
	for _, version := range []string{"1", "2"} {
		dir := filepath.Join(root, "Library", "plugins", "com.audit.synth@"+version)
		os.MkdirAll(filepath.Join(dir, "bin"), 0700)
		os.WriteFile(filepath.Join(dir, "bin", "synth.dll"), []byte("fixture"), 0600)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.audit.synth","name":"Synth","kind":"instrument","version":"`+version+`","dsp":{"entry":"synth.dll"}}`), 0600)
		source, _, err := soundlib.SyncInstalledMM(dir)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	if _, err := project.ApplyOps(pid, []map[string]any{{"op": "addTrack", "id": "stable-track", "name": "Track"}}); err != nil {
		t.Fatal(err)
	}
	stableID := project.LoadArrangementV2(pid)["tracks"].([]any)[0].(map[string]any)["id"].(string)
	var events []map[string]any
	env := toolEnvFor(pid, &events)
	result := loadPluginText(env, map[string]any{"id": "com.audit.synth", "track": stableID})
	if !strings.Contains(result, "多个版本") {
		t.Fatalf("ambiguous version accepted: %s", result)
	}
	result = loadPluginText(env, map[string]any{"id": sources[1].ID, "track": stableID})
	if strings.Contains(result, "错误") {
		t.Fatal(result)
	}
	arr := project.LoadArrangementV2(pid)
	tracks := arr["tracks"].([]any)
	track := tracks[0].(map[string]any)
	itemID := track["source"].(map[string]any)["itemId"]
	items := arr["rack"].(map[string]any)["items"].([]any)
	found := false
	for _, item := range items {
		data := item.(map[string]any)
		if data["id"] == itemID && data["sourceId"] == sources[1].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("registered instrument reference not persisted")
	}
	if len(events) != 1 || events[0]["rev"] == nil {
		t.Fatal("missing authoritative arrangement event")
	}
	before, _ := project.ReadArrangement(pid)
	result = loadPluginText(env, map[string]any{"id": sources[1].ID, "track": stableID, "slot": float64(0)})
	if !strings.Contains(result, "FX 槽不接受乐器") {
		t.Fatal(result)
	}
	after, _ := project.ReadArrangement(pid)
	if string(before) != string(after) {
		t.Fatal("rejected assignment changed project")
	}
}
