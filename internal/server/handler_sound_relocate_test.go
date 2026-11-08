package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"melodymate/internal/soundlib"
)

func addMissingSF2(t *testing.T, name, missingPath, hash string) soundlib.Source {
	t.Helper()
	src, err := soundlib.Add(soundlib.Source{
		Kind:   soundlib.KindSF2,
		Name:   name,
		Path:   missingPath,
		Hash:   hash,
		Origin: soundlib.OriginUser,
	})
	if err != nil {
		t.Fatalf("add source: %v", err)
	}
	return src
}

func TestRelocateSoundFontByHash(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()
	data := testSF2("Relocated", 1)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	missing := filepath.Join(root, "Library", "soundfonts", "old-name.sf2")
	src := addMissingSF2(t, "Relocated", missing, hash)

	moved := filepath.Join(root, "Library", "soundfonts", "new-name.sf2")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved, data, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{"id": src.ID}))
	if code != 200 {
		t.Fatalf("relocate failed: %d %v", code, out)
	}
	if got, _ := out["path"].(string); !samePath(got, moved) {
		t.Fatalf("relocated path=%q want %q", got, moved)
	}
	view, err := soundlib.Get(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(soundlib.ResolvePath(view.Path), moved) {
		t.Fatalf("registry path not updated: %q", view.Path)
	}
	if view.Runtime.Missing {
		t.Fatalf("relocated source still reported missing: %+v", view.Runtime)
	}
	if !stringsEqualFold(view.Hash, hash) {
		t.Fatalf("hash not updated: %q", view.Hash)
	}
}

func TestRelocateSoundFontByNameFallback(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()
	data := testSF2("ByName", 1)
	missing := filepath.Join(root, "Library", "soundfonts", "Piano.sf2")
	src := addMissingSF2(t, "ByName", missing, "")

	moved := filepath.Join(root, "Library", "soundfonts", "sub", "piano.SF2")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved, data, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{"id": src.ID}))
	if code != 200 {
		t.Fatalf("relocate by name failed: %d %v", code, out)
	}
	if got, _ := out["path"].(string); !samePath(got, moved) {
		t.Fatalf("relocated path=%q want %q", got, moved)
	}
}

func TestRelocateSoundFontExplicitPathAndHashMismatch(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()
	oldData := testSF2("Old", 1)
	oldSum := sha256.Sum256(oldData)
	src := addMissingSF2(t, "Old", filepath.Join(root, "Library", "soundfonts", "Old.sf2"), hex.EncodeToString(oldSum[:]))

	newData := testSF2("New", 2)
	other := filepath.Join(root, "elsewhere", "Other.sf2")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, newData, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{"id": src.ID, "path": other}))
	if code != 409 {
		t.Fatalf("expected 409 hash mismatch, got %d %v", code, out)
	}
	if out["code"] != "soundfont_hash_mismatch" {
		t.Fatalf("unexpected error code: %v", out)
	}

	code, out = doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{
		"id": src.ID, "path": other, "force": true,
	}))
	if code != 200 {
		t.Fatalf("forced relocate failed: %d %v", code, out)
	}
	view, err := soundlib.Get(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	newSum := sha256.Sum256(newData)
	if !stringsEqualFold(view.Hash, hex.EncodeToString(newSum[:])) {
		t.Fatalf("hash not replaced: %q", view.Hash)
	}
}

func TestRelocateSoundFontRejectsInvalid(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()
	src := addMissingSF2(t, "Bad", filepath.Join(root, "Library", "soundfonts", "Bad.sf2"), "")

	notSF2 := filepath.Join(root, "bad.wav")
	if err := os.WriteFile(notSF2, []byte("RIFFxxxxWAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{"id": src.ID, "path": notSF2})); code != 400 {
		t.Fatalf("non-sf2 path should be rejected, got %d", code)
	}
	if code, _ := doSound(t, r, "POST", "/api/sound/relocate", mustJSONBody(map[string]any{"id": "nope"})); code != 404 {
		t.Fatalf("unknown id should be 404, got %d", code)
	}
}

func mustJSONBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return filepath.ToSlash(a) == filepath.ToSlash(b) || equalFoldASCII(a, b)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestEnsureSF2SourceStoresContentHash(t *testing.T) {
	root := withTempSoundRoot(t)
	data := testSF2("Hashed", 1)
	path := filepath.Join(root, "Library", "soundfonts", "Hashed.sf2")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	src, created, err := soundlib.EnsureSF2Source(path, "Hashed")
	if err != nil || !created {
		t.Fatalf("ensure failed: created=%v err=%v", created, err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	if !stringsEqualFold(src.Hash, want) {
		t.Fatalf("hash not stored at import: got %q want %q", src.Hash, want)
	}
	// 再次登记同一路径：补哈希逻辑不应改变已有条目
	again, createdAgain, err := soundlib.EnsureSF2Source(path, "Hashed")
	if err != nil || createdAgain {
		t.Fatalf("re-ensure changed semantics: created=%v err=%v", createdAgain, err)
	}
	if again.ID != src.ID || !stringsEqualFold(again.Hash, want) {
		t.Fatalf("re-ensure mutated entry: %+v", again)
	}
}
