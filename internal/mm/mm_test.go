package mm

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPkg 造一个 .mm（zip）用于测试。files 为 路径→内容。
func buildPkg(t *testing.T, manifest any, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if manifest != nil {
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		w, _ := zw.Create("manifest.json")
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "test.mm")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseManifest() map[string]any {
	return map[string]any{
		"schema":  1,
		"id":      "com.example.myfilter",
		"name":    "My Filter",
		"version": "1.0.0",
		"kind":    "effect",
		"entry":   "win-x64/myfilter.dll",
		"api":     1,
		"params": []map[string]any{
			{"id": "cutoff", "name": "Cutoff", "min": 20, "max": 20000, "default": 1000, "curve": "log"},
		},
	}
}

func files(extra map[string]string) map[string]string {
	out := map[string]string{"bin/win-x64/myfilter.dll": "MZ fake dll"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func codes(res *Result) []string {
	out := make([]string, 0, len(res.Issues))
	for _, i := range res.Issues {
		out = append(out, i.Code)
	}
	return out
}

func hasCode(res *Result, code string) bool {
	for _, c := range codes(res) {
		if c == code {
			return true
		}
	}
	return false
}

func TestValidateAcceptsWellFormedPackage(t *testing.T) {
	p := buildPkg(t, baseManifest(), files(map[string]string{
		"ui/index.html": "<html></html>",
	}))
	// ui 未声明 → 页面存在也无妨
	res := ValidateFile(p)
	if !res.OK {
		t.Fatalf("合法包应通过，实际: %v", res.Error())
	}
	if res.Manifest == nil || res.Manifest.ID != "com.example.myfilter" {
		t.Fatalf("manifest 解析异常: %+v", res.Manifest)
	}
	if len(res.Files) != 3 {
		t.Fatalf("文件清单应有 3 项: %v", res.Files)
	}
}

func TestValidateRejectsNotZip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.mm")
	_ = os.WriteFile(p, []byte("not a zip"), 0o644)
	res := ValidateFile(p)
	if res.OK || !hasCode(res, "MM001") {
		t.Fatalf("非 zip 应报 MM001: %v", codes(res))
	}
}

func TestValidateRejectsMissingManifest(t *testing.T) {
	p := buildPkg(t, nil, files(nil))
	res := ValidateFile(p)
	if res.OK || !hasCode(res, "MM002") {
		t.Fatalf("缺 manifest 应报 MM002: %v", codes(res))
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	p := buildPkg(t, "not-an-object", files(nil))
	res := ValidateFile(p)
	if res.OK || !hasCode(res, "MM003") {
		t.Fatalf("非法 JSON 应报 MM003: %v", codes(res))
	}
}

func TestValidateRejectsFieldProblems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   string
	}{
		{"schema 过高", func(m map[string]any) { m["schema"] = 9 }, "MM004"},
		{"api 不匹配", func(m map[string]any) { m["api"] = 2 }, "MM005"},
		{"id 非法", func(m map[string]any) { m["id"] = "Bad_ID" }, "MM006"},
		{"name 为空", func(m map[string]any) { m["name"] = "" }, "MM006"},
		{"version 非 semver", func(m map[string]any) { m["version"] = "1.0" }, "MM006"},
		{"kind 非法", func(m map[string]any) { m["kind"] = "plugin" }, "MM003"},
		{"entry 不在包内", func(m map[string]any) { m["entry"] = "win-x64/nope.dll" }, "MM007"},
		{"entry 穿越", func(m map[string]any) { m["entry"] = "../../evil.dll" }, "MM008"},
		{"未知能力", func(m map[string]any) { m["capabilities"] = []string{"network2"} }, "MM009"},
		{"参数 default 越界", func(m map[string]any) {
			m["params"] = []map[string]any{{"id": "c", "min": 0, "max": 1, "default": 5}}
		}, "MM003"},
		{"参数 max<=min", func(m map[string]any) {
			m["params"] = []map[string]any{{"id": "c", "min": 1, "max": 1, "default": 1}}
		}, "MM003"},
		{"curve 非法", func(m map[string]any) {
			m["params"] = []map[string]any{{"id": "c", "min": 0, "max": 1, "default": 0.5, "curve": "bogus"}}
		}, "MM003"},
		{"ui 页面缺失", func(m map[string]any) {
			m["ui"] = map[string]any{"entry": "ui/missing.html", "width": 600, "height": 400}
		}, "MM012"},
		{"ui 尺寸非法", func(m map[string]any) {
			m["ui"] = map[string]any{"entry": "ui/index.html", "width": 9999}
		}, "MM012"},
		{"command 名非法", func(m map[string]any) {
			m["commands"] = []map[string]any{{"name": "BadName"}}
		}, "MM003"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseManifest()
			tc.mutate(m)
			extra := map[string]string{}
			if ui, ok := m["ui"]; ok {
				if um, isMap := ui.(map[string]any); isMap {
					if e, isStr := um["entry"].(string); isStr && e == "ui/index.html" {
						extra["ui/index.html"] = "<html></html>"
					}
				}
			}
			p := buildPkg(t, m, files(extra))
			res := ValidateFile(p)
			if res.OK {
				t.Fatalf("%s 应被拒绝", tc.name)
			}
			if !hasCode(res, tc.want) {
				t.Fatalf("%s 期望 %s，实际 %v", tc.name, tc.want, codes(res))
			}
		})
	}
}

func TestValidateRejectsPathTraversalEntries(t *testing.T) {
	p := buildPkg(t, baseManifest(), files(map[string]string{
		"../evil.dll":          "x",
		"/abs/evil.dll":        "x",
		"C:/Windows/evil.dll":  "x",
		"ui/../../escape.html": "x",
	}))
	res := ValidateFile(p)
	if res.OK || !hasCode(res, "MM008") {
		t.Fatalf("路径穿越应报 MM008: %v", codes(res))
	}
}

func TestValidateHashMismatchAndMatch(t *testing.T) {
	content := "MZ fake dll"
	sum := sha256.Sum256([]byte(content))
	good := hex.EncodeToString(sum[:])

	m := baseManifest()
	m["hash"] = map[string]any{"algorithm": "sha256", "files": map[string]string{"bin/win-x64/myfilter.dll": good}}
	if res := ValidateFile(buildPkg(t, m, files(nil))); !res.OK {
		t.Fatalf("哈希正确应通过: %v", res.Error())
	}

	m2 := baseManifest()
	m2["hash"] = map[string]any{"algorithm": "sha256", "files": map[string]string{"bin/win-x64/myfilter.dll": strings.Repeat("a", 64)}}
	res := ValidateFile(buildPkg(t, m2, files(nil)))
	if res.OK || !hasCode(res, "MM010") {
		t.Fatalf("哈希不匹配应报 MM010: %v", codes(res))
	}

	m3 := baseManifest()
	m3["hash"] = map[string]any{"algorithm": "sha256", "files": map[string]string{"bin/win-x64/myfilter.dll": "nothex"}}
	res = ValidateFile(buildPkg(t, m3, files(nil)))
	if res.OK || !hasCode(res, "MM010") {
		t.Fatalf("哈希格式非法应报 MM010: %v", codes(res))
	}

	m4 := baseManifest()
	m4["hash"] = map[string]any{"algorithm": "md5", "files": map[string]string{"bin/win-x64/myfilter.dll": good}}
	res = ValidateFile(buildPkg(t, m4, files(nil)))
	if res.OK || !hasCode(res, "MM010") {
		t.Fatalf("非 sha256 应报 MM010: %v", codes(res))
	}
}

func TestValidateRejectsOversizeFileCount(t *testing.T) {
	extra := map[string]string{}
	// 唯一名（map 会去重，必须显式编号）
	for i := 0; i < MaxFiles+10; i++ {
		extra[fmt.Sprintf("presets/p%05d.json", i)] = "{}"
	}
	p := buildPkg(t, baseManifest(), files(extra))
	res := ValidateFile(p)
	if res.OK || !hasCode(res, "MM011") {
		t.Fatalf("文件数超限应报 MM011: %v", codes(res))
	}
}

func TestIsAIFlag(t *testing.T) {
	m := baseManifest()
	m["author"] = map[string]any{"type": "ai"}
	p := buildPkg(t, m, files(nil))
	res := ValidateFile(p)
	if !res.OK {
		t.Fatalf("AI 插件包应通过: %v", res.Error())
	}
	if !res.Manifest.IsAI() {
		t.Fatal("author.type=ai 应识别为 AI 生成（声音管理显示徽标）")
	}
	m2 := baseManifest()
	res2 := ValidateFile(buildPkg(t, m2, files(nil)))
	if res2.Manifest.IsAI() {
		t.Fatal("未声明 author 不应识别为 AI 生成")
	}
}

func TestSafeExtractRejectsTraversal(t *testing.T) {
	p := buildPkg(t, baseManifest(), files(map[string]string{"../evil.dll": "x"}))
	if err := SafeExtract(p, t.TempDir()); err == nil {
		t.Fatal("解包前置校验应拒绝穿越路径")
	}
	ok := buildPkg(t, baseManifest(), files(nil))
	if err := SafeExtract(ok, t.TempDir()); err != nil {
		t.Fatalf("合法包的前置校验不应报错: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
