// Package update 提供应用内自动更新支持：
// 拉取 R2 公开域名上的更新清单（update.json）、版本比较、安装包下载状态机。
// 设计原则：清单拉取失败一律放行（绝不把离线用户锁死在应用外）。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manifest 更新清单（桶根 update.json）
type Manifest struct {
	Version       string            `json:"version"`                  // 最新版本号（如 3.0.3）
	Date          string            `json:"date"`                     // 更新日期（如 2026-09-05）
	Mandatory     bool              `json:"mandatory"`                // 必要更新：旧版本必须升级才能继续使用
	Notes         string            `json:"notes"`                    // 更新日志（markdown）
	Downloads     map[string]string `json:"downloads"`                // 按 GOOS 索引的下载链接：windows / linux
	SHA256Windows string            `json:"sha256_windows,omitempty"` // Windows 安装包 sha256（存在即校验）
}

// DownloadURLFor 按 GOOS 取下载链接；未知平台返回空串
func (m *Manifest) DownloadURLFor(goos string) string {
	if m == nil || m.Downloads == nil {
		return ""
	}
	return m.Downloads[goos]
}

// CompareVersions 按点分段数值比较版本号（"v" 前缀容忍，段数不齐按 0 补）。
// 返回 1 / 0 / -1（a 分别大于/等于/小于 b）。
func CompareVersions(a, b string) int {
	pa := versionSegments(a)
	pb := versionSegments(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	return 0
}

// versionSegments 解析 "v3.0.3" → [3,0,3]；非数字段按 0 处理
func versionSegments(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.ToLower(v), "v")
	parts := strings.Split(v, ".")
	segs := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		segs = append(segs, n)
	}
	return segs
}

// IsValidVersionString 版本号是否可安全用于文件名：仅允许数字与点
// （可选 v 前缀），且每段必须非空（拒绝 ".." 之类纯点串）。
// 清单来自远程，未消毒的版本号拼入落盘路径会构成路径穿越。
func IsValidVersionString(v string) bool {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.ToLower(v), "v")
	if v == "" {
		return false
	}
	for _, seg := range strings.Split(v, ".") {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// Client 更新清单拉取客户端（带进程内缓存与降级）
type Client struct {
	url      string
	http     *http.Client
	ttl      time.Duration
	mu       sync.Mutex
	cached   *Manifest
	cachedAt time.Time
}

// NewClient 创建清单客户端。url 为公开可读的 update.json 完整地址。
func NewClient(url string) *Client {
	return &Client{
		url:  url,
		http: &http.Client{Timeout: 5 * time.Second},
		ttl:  10 * time.Minute,
	}
}

// Fetch 拉取清单：TTL 内直接返回缓存；网络失败时降级返回旧缓存（若有），
// 让强制更新判定不因瞬时网络抖动消失。仅当从未成功拉取时返回错误。
func (c *Client) Fetch(ctx context.Context) (*Manifest, error) {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < c.ttl {
		m := c.cached
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	m, err := c.fetchOnce(ctx)
	if err != nil {
		c.mu.Lock()
		if c.cached != nil {
			m := c.cached
			c.mu.Unlock()
			return m, nil // 降级：用旧缓存（更新判定保持可用）
		}
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	c.cached = m
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return m, nil
}

// Cached 返回最近一次成功拉取的清单（可能为 nil；不做 TTL 判断，
// 强制更新网关用它做持续判定）。
func (c *Client) Cached() *Manifest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached
}

func (c *Client) fetchOnce(ctx context.Context) (*Manifest, error) {
	// 清单必须新鲜：自定义域名有 CDN 缓存，对象更新后边缘节点可能继续
	// 返回旧内容——加唯一查询参数绕开边缘缓存（对象元数据另带
	// Cache-Control: no-store，双保险）
	fresh := c.url
	bust := "_u=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if strings.Contains(fresh, "?") {
		fresh += "&" + bust
	} else {
		fresh += "?" + bust
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fresh, nil)
	if err != nil {
		return nil, fmt.Errorf("构造更新清单请求失败: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取更新清单失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新清单返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取更新清单失败: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("解析更新清单失败: %w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("更新清单缺少 version 字段")
	}
	return &m, nil
}
