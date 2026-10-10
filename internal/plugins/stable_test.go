package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStableTrackStateSurvivesReorder(t *testing.T) {
	s := NewStore(t.TempDir())
	a := Target{ProjectID: "P", TrackID: "track/one", Track: 0, Slot: -1}
	if err := s.Save(a, "gain@1", "mm", []byte("state")); err != nil {
		t.Fatal(err)
	}
	a.Track = 2
	env, err := s.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	if env.TrackID != a.TrackID || !env.Matches("gain@1") || env.Matches("") {
		t.Fatal("unsafe state identity", env)
	}
	b := a
	b.TrackID = "other"
	if _, err = s.Load(b); err == nil {
		t.Fatal("state restored to different track")
	}
}

func TestPluginReplacementKeepsBothStates(t *testing.T) {
	store := NewStore(t.TempDir())
	target := Target{ProjectID: "P", TrackID: "A", Track: 0, Slot: -1}
	if err := store.Save(target, "synth@1", "mm", []byte("original synth")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(target, "vst@2", "vst3", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	original, err := store.LoadMatching(target, "synth@1", "mm")
	if err != nil {
		t.Fatal("replacement discarded original state", err)
	}
	payload, _ := original.Payload()
	if string(payload) != "original synth" {
		t.Fatal("original state changed")
	}
	current, err := store.LoadMatching(target, "vst@2", "vst3")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = current.Payload()
	if string(payload) != "replacement" {
		t.Fatal("replacement state changed")
	}
	backup := filepath.Join(store.Dir(), target.FileName()+".before-reference-change")
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal("original backup missing", err)
	}
	if err = store.Save(target, "vst@2", "vst3", []byte("edited replacement")); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(backup)
	if !bytes.Equal(raw, again) {
		t.Fatal("repeat rewrote original backup")
	}
}
func TestLegacyStateMigrationRequiresOwnershipAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	legacy := Target{ProjectID: "P", Track: 1, Slot: 0}
	if err := store.Save(legacy, "gain@1", "mm", []byte("state")); err != nil {
		t.Fatal(err)
	}
	target := legacy
	target.TrackID = "stable"
	if _, err := store.LoadMatching(target, "gain@1", "mm"); err == nil {
		t.Fatal("unknown mapping accepted")
	}
	path := filepath.Join(store.Dir(), legacy.FileName())
	raw, _ := os.ReadFile(path)
	var env Envelope
	json.Unmarshal(raw, &env)
	env.TrackID = target.TrackID
	original, _ := json.Marshal(env)
	os.WriteFile(path, original, 0600)
	if _, err := store.LoadMatching(target, "other@1", "mm"); err == nil {
		t.Fatal("different reference migrated")
	}
	if _, err := store.LoadMatching(target, "gain@1", "mm"); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".before-stable-id")
	if err != nil || !bytes.Equal(original, backup) {
		t.Fatal("original backup absent")
	}
	newPath := filepath.Join(store.Dir(), target.FileName())
	before, _ := os.Stat(newPath)
	if _, err := store.LoadMatching(target, "gain@1", "mm"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(newPath)
	if before.ModTime() != after.ModTime() {
		t.Fatal("repeat rewrote migrated state")
	}
}
