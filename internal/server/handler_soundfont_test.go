package server

import (
	"bytes"
	"encoding/binary"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"melodymate/internal/config"
)

// minimalSF2 构造一个 12 字节的合法 RIFF/sfbk 外壳（声明长度与实收一致）。
func minimalSF2(payload string) []byte {
	body := []byte(payload)
	total := 12 + len(body)
	buf := make([]byte, total)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(total-8))
	copy(buf[8:12], "sfbk")
	copy(buf[12:], body)
	return buf
}

func withSoundFontDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib := config.ProjectRoot, config.LibraryDir
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	t.Cleanup(func() { config.ProjectRoot, config.LibraryDir = oldRoot, oldLib })
	return config.LibraryDir
}

func TestSaveSF2UploadStreamsAndDedupes(t *testing.T) {
	withSoundFontDir(t)
	data := minimalSF2("hello")

	req := httptest.NewRequest("POST", "/api/audio/soundfonts?name=Piano.sf2", bytes.NewReader(data))
	dest, size, uerr := saveSF2Upload(req, "Piano.sf2")
	if uerr != nil {
		t.Fatalf("save failed: %+v", uerr)
	}
	if size != int64(len(data)) {
		t.Fatalf("size=%d want %d", size, len(data))
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("written file mismatch: %v", err)
	}

	// 同一份内容重复导入：沿用同一路径，不产生副本
	req2 := httptest.NewRequest("POST", "/api/audio/soundfonts?name=Piano.sf2", bytes.NewReader(data))
	dest2, _, uerr2 := saveSF2Upload(req2, "Piano.sf2")
	if uerr2 != nil {
		t.Fatalf("second save failed: %+v", uerr2)
	}
	if dest2 != dest {
		t.Fatalf("identical import should reuse %q, got %q", dest, dest2)
	}

	// 同名不同内容：不覆盖，落到唯一文件名（S-M5）
	other := minimalSF2("different")
	req3 := httptest.NewRequest("POST", "/api/audio/soundfonts?name=Piano.sf2", bytes.NewReader(other))
	dest3, _, uerr3 := saveSF2Upload(req3, "Piano.sf2")
	if uerr3 != nil {
		t.Fatalf("third save failed: %+v", uerr3)
	}
	if dest3 == dest {
		t.Fatal("different content must not overwrite the existing file")
	}
	if !strings.Contains(filepath.Base(dest3), "(2)") {
		t.Fatalf("expected unique suffix, got %q", dest3)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("original file was modified: %v", err)
	}
}

func TestSaveSF2UploadRejectsInvalid(t *testing.T) {
	withSoundFontDir(t)

	cases := []struct {
		name string
		data []byte
		code int
	}{
		{"magic", []byte("NOTASF2FILE!!"), 400},
		{"truncated", minimalSF2("abc")[:14], 400},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/x", bytes.NewReader(c.data))
		if _, _, uerr := saveSF2Upload(req, "bad.sf2"); uerr == nil || uerr.status != c.code {
			t.Fatalf("%s: expected status %d, got %+v", c.name, c.code, uerr)
		}
	}

	req := httptest.NewRequest("POST", "/x", bytes.NewReader(minimalSF2("x")))
	if _, _, uerr := saveSF2Upload(req, "bad.sf3"); uerr == nil || uerr.status != 400 {
		t.Fatalf("sf3 should be rejected, got %+v", uerr)
	}
}
