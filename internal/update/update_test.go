package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCompareVersions 版本比较：数值分段（非字典序）、v 前缀容忍、段数不齐补零
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"3.0.3", "3.0.2", 1},
		{"3.0.2", "3.0.3", -1},
		{"3.0.3", "3.0.3", 0},
		{"3.0.2", "3.0.10", -1}, // 数值比较：2 < 10（字典序会错判）
		{"v3.0.3", "3.0.3", 0},  // v 前缀容忍
		{"3.0", "3.0.0", 0},     // 段数不齐按 0 补
		{"3", "2.9.9", 1},
		{"", "0.0.1", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestFetchAndCache 清单拉取：正常解析、TTL 内走缓存、字段缺失报错
func TestFetchAndCache(t *testing.T) {
	version := "9.9.9"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + version + `","date":"2026-09-05","mandatory":true,` +
			`"notes":"# 日志\n- 修复若干","downloads":{"windows":"https://x/e.exe","linux":"https://x/t.gz"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	m, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if m.Version != "9.9.9" || !m.Mandatory || m.DownloadURLFor("windows") != "https://x/e.exe" {
		t.Fatalf("清单解析不正确: %+v", m)
	}

	// TTL 内改服务端内容：应命中缓存
	version = "0.0.1"
	m2, err := c.Fetch(context.Background())
	if err != nil || m2.Version != "9.9.9" {
		t.Fatalf("TTL 内应返回缓存: m2=%+v err=%v", m2, err)
	}
	if c.Cached() != m2 {
		t.Fatalf("Cached() 应与最近一次拉取一致")
	}
}

// TestFetchFailureFallsBackToStale 网络失败时降级返回旧缓存（强制更新判定不因抖动消失）
func TestFetchFailureFallsBackToStale(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"4.0.0","mandatory":true}`))
	}))
	c := NewClient(good.URL)
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}
	good.Close() // 之后全部失败

	m, err := c.Fetch(context.Background())
	if err != nil || m.Version != "4.0.0" {
		t.Fatalf("网络失败应降级返回旧缓存: m=%+v err=%v", m, err)
	}
}

// TestFetchInvalid 清单缺失 version 字段时报错
func TestFetchInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"date":"2026-09-05"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatalf("缺 version 应报错")
	}
}

// TestDownloaderSuccessAndSHA 下载状态机：成功落位 + sha256 校验 + 进度快照
func TestDownloaderSuccessAndSHA(t *testing.T) {
	payload := strings.Repeat("AI_MIDI", 4096) // 28KB
	sum := sha256.Sum256([]byte(payload))
	sha := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	launched := make(chan string, 1)
	d := NewDownloader(func(path string) error { launched <- path; return nil })
	dest := filepath.Join(t.TempDir(), "setup.exe")

	if err := d.Start(context.Background(), srv.URL, dest, sha); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitState(t, d, StateLaunched, 3*time.Second)

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("安装包未落位: %v", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part 临时文件应已清理")
	}
	select {
	case p := <-launched:
		if p != dest {
			t.Fatalf("onLaunch 路径不正确: %s", p)
		}
	case <-time.After(time.Second):
		t.Fatalf("onLaunch 未被调用")
	}
}

// TestDownloaderSHAMismatch sha256 不匹配 → Failed，且不调用 onLaunch
func TestDownloaderSHAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("corrupted-content"))
	}))
	defer srv.Close()

	launched := false
	d := NewDownloader(func(path string) error { launched = true; return nil })
	dest := filepath.Join(t.TempDir(), "setup.exe")

	_ = d.Start(context.Background(), srv.URL, dest, strings.Repeat("0", 64))
	waitState(t, d, StateFailed, 3*time.Second)
	if d.Snapshot().Error == "" {
		t.Fatalf("失败状态应携带错误信息")
	}
	if launched {
		t.Fatalf("校验失败不应启动安装包")
	}
}

// TestDownloaderHTTPError 源站 500 → Failed
func TestDownloaderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := NewDownloader(nil)
	dest := filepath.Join(t.TempDir(), "setup.exe")
	_ = d.Start(context.Background(), srv.URL, dest, "")
	waitState(t, d, StateFailed, 3*time.Second)
}

// TestDownloaderSizeLimit 体积超限（注入小上限）→ Failed
func TestDownloaderSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	d := NewDownloader(nil)
	d.maxBytes = 1024
	dest := filepath.Join(t.TempDir(), "setup.exe")
	_ = d.Start(context.Background(), srv.URL, dest, "")
	waitState(t, d, StateFailed, 3*time.Second)
	if !strings.Contains(d.Snapshot().Error, "体积上限") {
		t.Fatalf("应报体积超限: %q", d.Snapshot().Error)
	}
}

// TestDownloaderSingleFlight 下载进行中重复 Start 返回 ErrInProgress
func TestDownloaderSingleFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	d := NewDownloader(nil)
	dest := filepath.Join(t.TempDir(), "setup.exe")
	if err := d.Start(context.Background(), srv.URL, dest, ""); err != nil {
		t.Fatalf("首次 Start 失败: %v", err)
	}
	if err := d.Start(context.Background(), srv.URL, dest, ""); err != ErrInProgress {
		t.Fatalf("进行中重复 Start 应返回 ErrInProgress, got %v", err)
	}
	close(release)                                 // 放行下载，让首个请求完成
	waitState(t, d, StateCompleted, 3*time.Second) // 无 onLaunch：止步于 completed
	if d.Snapshot().Path != dest {
		t.Fatalf("完成状态应携带落位路径: %+v", d.Snapshot())
	}
}

func waitState(t *testing.T, d *Downloader, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Snapshot().State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待状态 %s 超时，当前 %s (%+v)", want, d.Snapshot().State, d.Snapshot())
}
