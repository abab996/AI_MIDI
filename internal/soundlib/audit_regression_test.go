package soundlib

import (
	"melodymate/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestSDKLegacyEntryResolvesToInstalledLibrary(t *testing.T) {
	withTempRoot(t)
	dir := filepath.Join(config.LibraryDir, "plugins", "com.sdk.synth@1")
	if err := os.MkdirAll(filepath.Join(dir, "bin", "win-x64"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "win-x64", "synth.dll"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.sdk.synth","name":"Synth","version":"1","kind":"instrument","entry":"win-x64/synth.dll"}`), 0644); err != nil {
		t.Fatal(err)
	}
	src, _, err := SyncInstalledMM(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := ResolvePlugin(src.ID)
	if err != nil || ref.Path != filepath.Join(dir, "bin", "win-x64", "synth.dll") {
		t.Fatalf("SDK library unresolved: %+v, %v", ref, err)
	}
}

func TestMMPathAndDisabledRefresh(t *testing.T) {
	oldRoot, oldLib := config.ProjectRoot, config.LibraryDir
	config.ProjectRoot = t.TempDir()
	config.LibraryDir = filepath.Join(config.ProjectRoot, "Library")
	defer func() { config.ProjectRoot = oldRoot; config.LibraryDir = oldLib }()
	dir := filepath.Join(config.LibraryDir, "plugins", "com.test.audit@1")
	makeInstalledMM(t, dir, "com.test.audit", "Audit", "1", "human", nil, "")
	src, _, err := SyncInstalledMM(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ResolvePath(src.Path) != dir {
		t.Fatalf("wrong storage root: %s", ResolvePath(src.Path))
	}
	_, err = Update(src.ID, func(s *Source) error { s.Enabled = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	src, _, err = SyncInstalledMM(dir)
	if err != nil || src.Enabled {
		t.Fatalf("refresh re-enabled disabled source: %v %v", src.Enabled, err)
	}
	file := FilePath()
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	SyncInstalledMM(dir)
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeat MM sync rewrote registry")
	}
}
func TestMigrationExcludesManagedPack(t *testing.T) {
	oldRoot, oldLib, oldSettings := config.ProjectRoot, config.LibraryDir, config.SettingsFile
	root := t.TempDir()
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.SettingsFile = filepath.Join(root, "settings.json")
	defer func() { config.ProjectRoot = oldRoot; config.LibraryDir = oldLib; config.SettingsFile = oldSettings }()
	dir := filepath.Join(root, "samples")
	os.MkdirAll(dir, 0755)
	src, err := Add(Source{Kind: KindSamplePack, Name: "pack", Dirs: []string{dir}, Origin: OriginUser, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	if _, err = Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range List(KindSamplePack, "") {
		if p.ID != src.ID && len(p.Dirs) > 0 {
			t.Fatal("migration duplicated managed directory")
		}
	}
	_, err = Update(src.ID, func(s *Source) error { s.Enabled = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	Migrate()
	if len(config.LoadSettings().MaterialDirs) != 0 {
		t.Fatal("disabled pack remains mirrored")
	}
}
