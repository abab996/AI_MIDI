package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// 用户知识文件上传防线回归（源码文本护栏，仿 TestSoundFontUploadLimit）：
// handler 层必须保留 2MB 上限、.md/.txt 强制、UTF-8 校验与 isSubPath 白名单；
// router 层必须注册 /api/library/ 且 requestLimitFor 显式设 2MB——该 case
// 必须先于通配的 "*/files" 256MB case，否则上传上限被放大 128 倍
func TestLibraryUploadGuards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "server", "handler_library.go"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	for _, frag := range []string{
		"2 << 20",      // handler 层大小上限
		`".md"`, `".txt"`, // 扩展名强制
		"utf8.Valid", // 非 UTF-8 拒绝
		"isSubPath",  // 路径白名单
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("handler_library.go 缺少防护点 %s", frag)
		}
	}

	routerData, err := os.ReadFile(filepath.Join("..", "..", "internal", "server", "router.go"))
	if err != nil {
		t.Skip(err.Error())
	}
	if !bytes.Contains(routerData, []byte(`"/api/library/"`)) {
		t.Fatal("router.go 应注册 /api/library/ 子路由")
	}
	if !bytes.Contains(routerData, []byte(`HasPrefix(path, "/api/library/")`)) {
		t.Fatal("router.go requestLimitFor 应为 /api/library/ 显式设 2MB 上限（先于 */files 通配 case）")
	}
}

// libraryHandlers 独立 mux 直挂被测子路由（handleLibrarySub 只依赖 config
// 与包内工具，零值 Router 即可用）
func libraryHandlers(t *testing.T) (*http.ServeMux, func()) {
	t.Helper()
	dir := t.TempDir()
	oldLib, oldUser := config.LibraryDir, config.LibraryUserDir
	config.LibraryDir = dir
	config.LibraryUserDir = filepath.Join(dir, "user")
	cleanup := func() { config.LibraryDir, config.LibraryUserDir = oldLib, oldUser }

	r := &Router{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/library/", r.handleLibrarySub)
	return mux, cleanup
}

func doLibraryReq(mux *http.ServeMux, method, target string, body []byte) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// 全链路行为回归：上传（含各种拒绝）、列表、预览、重命名、删除
func TestLibraryHandlersCRUD(t *testing.T) {
	mux, cleanup := libraryHandlers(t)
	defer cleanup()

	name := url.QueryEscape("爵士和声.md")

	// 上传成功并落盘到 Library/user/
	if w := doLibraryReq(mux, "POST", "/api/library/files?name="+name, []byte("# 爵士和声\n内容")); w.Code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(config.LibraryUserDir, "爵士和声.md")); err != nil {
		t.Fatalf("文件未落盘到 Library/user/: %v", err)
	}

	// 非 .md/.txt 拒绝
	if w := doLibraryReq(mux, "POST", "/api/library/files?name=evil.exe", []byte("x")); w.Code != http.StatusBadRequest {
		t.Errorf("非 .md/.txt 应 400: %d", w.Code)
	}
	// 非 UTF-8 拒绝（GBK 的「中」）
	if w := doLibraryReq(mux, "POST", "/api/library/files?name=gbk.md", []byte{0xD6, 0xD0}); w.Code != http.StatusBadRequest {
		t.Errorf("非 UTF-8 应 400: %d", w.Code)
	}
	// 空文件拒绝
	if w := doLibraryReq(mux, "POST", "/api/library/files?name=empty.md", nil); w.Code != http.StatusBadRequest {
		t.Errorf("空文件应 400: %d", w.Code)
	}
	// 超过 2MB 拒绝（handler 层 MaxBytesReader）
	big := make([]byte, (2<<20)+1)
	if w := doLibraryReq(mux, "POST", "/api/library/files?name=big.md", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超 2MB 应 413: %d", w.Code)
	}

	// 列表：user 组收录、builtin 组不混入用户文件
	w := doLibraryReq(mux, "GET", "/api/library/files", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("列表失败: %d", w.Code)
	}
	var listing struct {
		Builtin []map[string]any `json:"builtin"`
		User    []map[string]any `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatalf("列表 JSON 解析失败: %v", err)
	}
	if len(listing.User) != 1 || listing.User[0]["name"] != "爵士和声.md" {
		t.Fatalf("列表 user 组不符: %v", listing.User)
	}

	// 预览（用户范围）
	w = doLibraryReq(mux, "GET", "/api/library/files/content?scope=user&name="+name, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "爵士和声") {
		t.Fatalf("预览失败: %d %s", w.Code, w.Body.String())
	}

	// 重命名 → 新名；再重命名到已存在的名字 → 409
	if w := doLibraryReq(mux, "POST", "/api/library/files/rename",
		[]byte(`{"from":"爵士和声.md","to":"爵士和声进阶.txt"}`)); w.Code != http.StatusOK {
		t.Fatalf("重命名失败: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(config.LibraryUserDir, "爵士和声进阶.txt")); err != nil {
		t.Fatalf("重命名未生效: %v", err)
	}
	if w := doLibraryReq(mux, "POST", "/api/library/files/rename",
		[]byte(`{"from":"爵士和声进阶.txt","to":"爵士和声进阶.txt"}`)); w.Code != http.StatusConflict {
		t.Errorf("重命名到自身（目标已存在）应 409: %d", w.Code)
	}

	// 删除 → 文件消失，预览 404
	if w := doLibraryReq(mux, "DELETE", "/api/library/files?name="+url.QueryEscape("爵士和声进阶.txt"), nil); w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	if w := doLibraryReq(mux, "GET", "/api/library/files/content?scope=user&name="+url.QueryEscape("爵士和声进阶.txt"), nil); w.Code != http.StatusNotFound {
		t.Errorf("删除后预览应 404: %d", w.Code)
	}

	// 写操作锁死在 Library/user：文件名消毒后不可能携带路径成分，
	// 内置目录文件（ Library 根）对删除端点不可达
	if err := os.MkdirAll(config.LibraryUserDir, 0o755); err != nil {
		t.Fatal(err)
	}
	builtin := filepath.Join(config.LibraryDir, "01_乐理基础.md")
	if err := os.WriteFile(builtin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(builtin); err != nil {
		t.Fatal(err)
	}
	_ = doLibraryReq(mux, "DELETE", "/api/library/files?name="+url.QueryEscape("01_乐理基础.md"), nil)
	if _, err := os.Stat(builtin); err != nil {
		t.Fatalf("内置文件不应可被删除端点触及: %v", err)
	}
}
