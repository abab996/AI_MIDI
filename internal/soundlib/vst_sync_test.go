package soundlib

import (
	"melodymate/internal/engine"
	"os"
	"path/filepath"
	"testing"
)

func TestVSTScanRegistersInstrumentsAndRetainsDisabledLegacySource(t *testing.T) {
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "Synth.vst3")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Add(Source{Kind: KindVST3, Name: "User name", Path: path, ClassUID: "synth-uid", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	list := []engine.PluginInfo{{ID: "VST3|" + path + "|synth-uid", Name: "Synth", Path: path, Category: "instrument"}}
	if err = SyncVST3Plugins(list); err != nil {
		t.Fatal(err)
	}
	if err = SyncVST3Plugins(list); err != nil {
		t.Fatal(err)
	}
	all := List(KindVST3, "")
	if len(all) != 1 {
		t.Fatalf("repeat duplicated sources: %v", all)
	}
	got, err := Get(legacy.ID)
	if err != nil || got.Enabled || got.Name != "User name" || got.ClassUID != list[0].ID || got.Category != "instrument" {
		t.Fatalf("legacy source changed incorrectly: %+v %v", got, err)
	}
}
