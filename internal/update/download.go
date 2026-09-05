package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// State 下载状态机状态
type State string

const (
	StateIdle        State = "idle"
	StateDownloading State = "downloading"
	StateCompleted   State = "completed" // 下载完成（onLaunch 可能仍在进行）
	StateFailed      State = "failed"
	StateLaunched    State = "launched" // 安装包已启动
)

// DefaultMaxBytes 下载体积上限（200MB，正常安装包 ~12MB，防异常响应撑爆磁盘）
const DefaultMaxBytes int64 = 200 << 20

// ErrInProgress 已有下载在进行中（Start 单飞）
var ErrInProgress = errors.New("下载已在进行中")

// Snapshot 下载进度快照（JSON 直接返回给前端轮询）
type Snapshot struct {
	State      State  `json:"state"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
	Path       string `json:"path,omitempty"`
	Launched   bool   `json:"launched"`
}

// Downloader 安装包下载状态机（单飞：同一时刻至多一个下载）。
// onLaunch 在下载校验成功后被调用（如运行安装程序），为 nil 则止步于 Completed。
type Downloader struct {
	http     *http.Client
	maxBytes int64
	onLaunch func(path string) error

	mu         sync.Mutex
	state      State
	downloaded int64
	total      int64
	err        error
	path       string
	launched   bool
	cancel     context.CancelFunc
}

// NewDownloader 创建下载器；onLaunch 为 nil 时下载完成后仅置 Completed。
func NewDownloader(onLaunch func(path string) error) *Downloader {
	return &Downloader{
		http:     &http.Client{Timeout: 0}, // 大文件无整体超时；连接层超时由 Transport 默认值承担
		maxBytes: DefaultMaxBytes,
		onLaunch: onLaunch,
		state:    StateIdle,
	}
}

// Start 启动下载（单飞：进行中重复调用返回 ErrInProgress）。
// destPath 为最终落位路径；下载中写 destPath+".part"，成功后原子改名。
// wantSHA 非空（32 字节 hex）时校验文件 sha256，不匹配按失败处理。
func (d *Downloader) Start(parent context.Context, url, destPath, wantSHA string) error {
	d.mu.Lock()
	if d.state == StateDownloading {
		d.mu.Unlock()
		return ErrInProgress
	}
	ctx, cancel := context.WithCancel(parent)
	d.cancel = cancel
	d.state = StateDownloading
	d.downloaded = 0
	d.total = 0
	d.err = nil
	d.path = ""
	d.launched = false
	d.mu.Unlock()

	go d.run(ctx, url, destPath, wantSHA)
	return nil
}

func (d *Downloader) run(ctx context.Context, url, destPath, wantSHA string) {
	fail := func(format string, a ...any) {
		err := fmt.Errorf(format, a...)
		d.mu.Lock()
		d.state = StateFailed
		d.err = err
		d.mu.Unlock()
		_ = os.Remove(destPath + ".part")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		fail("创建下载目录失败: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fail("构造下载请求失败: %v", err)
		return
	}
	resp, err := d.http.Do(req)
	if err != nil {
		fail("下载失败: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail("下载返回 %d", resp.StatusCode)
		return
	}

	total := resp.ContentLength
	if total > d.maxBytes {
		fail("文件超过体积上限（%d MB）", d.maxBytes>>20)
		return
	}

	partPath := destPath + ".part"
	part, err := os.Create(partPath)
	if err != nil {
		fail("创建临时文件失败: %v", err)
		return
	}

	hasher := sha256.New()
	counting := &countingWriter{w: io.MultiWriter(part, hasher), d: d}
	_, copyErr := io.Copy(counting, io.LimitReader(resp.Body, d.maxBytes+1))
	closeErr := part.Close()
	d.setTotal(total)

	if copyErr != nil {
		fail("下载中断: %v", copyErr)
		return
	}
	if closeErr != nil {
		fail("写入临时文件失败: %v", closeErr)
		return
	}
	if d.snapshot().Downloaded > d.maxBytes {
		fail("文件超过体积上限（%d MB）", d.maxBytes>>20)
		return
	}

	if wantSHA != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, strings.TrimSpace(wantSHA)) {
			fail("安装包校验失败（sha256 不匹配），文件可能已损坏")
			return
		}
	}

	_ = os.Remove(destPath) // Windows 下 Rename 不能覆盖已存在目标
	if err := os.Rename(partPath, destPath); err != nil {
		fail("落位安装包失败: %v", err)
		return
	}

	d.mu.Lock()
	d.state = StateCompleted
	d.path = destPath
	d.mu.Unlock()

	if d.onLaunch != nil {
		if err := d.onLaunch(destPath); err != nil {
			// 安装包已下载成功，启动失败不回滚下载（用户可手动运行该文件）
			d.mu.Lock()
			d.err = fmt.Errorf("启动安装程序失败: %v", err)
			d.mu.Unlock()
			return
		}
		d.mu.Lock()
		d.state = StateLaunched
		d.launched = true
		d.mu.Unlock()
	}
}

// Snapshot 返回当前进度快照
func (d *Downloader) Snapshot() Snapshot {
	return d.snapshot()
}

func (d *Downloader) snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{State: d.state, Downloaded: d.downloaded, Total: d.total, Path: d.path, Launched: d.launched}
	if d.err != nil {
		s.Error = d.err.Error()
	}
	return s
}

func (d *Downloader) setTotal(total int64) {
	d.mu.Lock()
	d.total = total
	d.mu.Unlock()
}

// countingWriter 统计已下载字节数并同步计算 sha256（实时进度）。
type countingWriter struct {
	w io.Writer
	d *Downloader
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.d.mu.Lock()
	c.d.downloaded += int64(n)
	c.d.mu.Unlock()
	return n, err
}
