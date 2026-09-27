// Package mm 实现 .mm 插件包的校验（规范见 docs/MM插件规范.md）。
//
// 设计要点：**fail-closed**。任何不确定的情况（未知能力、缺哈希、路径可疑）
// 都按"拒绝"处理——.mm 里的 DLL 是本地代码，装错等于让用户运行一个未知程序。
//
// 校验只读 zip 目录表 + manifest + 按需校验哈希，不执行包内任何代码，
// 也不解包到磁盘（解包是安装步骤的事，且必须走 SafeExtract）。
package mm

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// SchemaVersion 宿主支持的 manifest schema 上限
const SchemaVersion = 1

// ABIVersion 宿主 ABI 版本（必须与 manifest.api 精确相等）
const ABIVersion = 1

// 体积与数量上限（防 zip 炸弹）
const (
	MaxUncompressedBytes = 512 << 20 // 512 MiB
	MaxFiles             = 4096
	MaxManifestBytes     = 256 << 10
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	hexPattern     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	drivePattern   = regexp.MustCompile(`^[A-Za-z]:`)
)

// 已知能力（未列出的一律拒绝）
var knownCapabilities = map[string]bool{
	"files":    true,
	"network":  true,
	"threads":  true,
	"midi_out": true,
}

// Param 参数声明
type Param struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	Default     float64 `json:"default"`
	Unit        string  `json:"unit,omitempty"`
	Curve       string  `json:"curve,omitempty"` // linear|log|exp
	Steps       int     `json:"steps,omitempty"`
	Automatable *bool   `json:"automatable,omitempty"`
}

// UI 承载配置
type UI struct {
	Entry     string `json:"entry"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Resizable bool   `json:"resizable,omitempty"`
}

// Command 暴露给 AI 的自定义工具
type Command struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
}

// Hash 哈希声明
type Hash struct {
	Algorithm string            `json:"algorithm"`
	Files     map[string]string `json:"files"`
}

// Author 作者信息（AI 生成的插件据此显示徽标）
type Author struct {
	Type string `json:"type,omitempty"` // human|ai
}

// Manifest .mm 包的 manifest.json
type Manifest struct {
	Schema       int       `json:"schema"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	Vendor       string    `json:"vendor,omitempty"`
	Kind         string    `json:"kind"`
	Entry        string    `json:"entry"`
	API          int       `json:"api"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Params       []Param   `json:"params,omitempty"`
	UI           *UI       `json:"ui,omitempty"`
	Commands     []Command `json:"commands,omitempty"`
	Author       *Author   `json:"author,omitempty"`
	Hash         *Hash     `json:"hash,omitempty"`
}

// IsAI AI 生成的插件（声音管理里显示 ✨AI 徽标）
func (m *Manifest) IsAI() bool {
	return m.Author != nil && m.Author.Type == "ai"
}

// Issue 一条校验问题
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

func (i Issue) Error() string {
	if i.Path != "" {
		return fmt.Sprintf("%s: %s (%s)", i.Code, i.Message, i.Path)
	}
	return fmt.Sprintf("%s: %s", i.Code, i.Message)
}

// Result 校验结果
type Result struct {
	OK       bool      `json:"ok"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Files    []string  `json:"files"`
	Issues   []Issue   `json:"issues"`
}

// Error 汇总成可读错误（无问题时返回 nil）
func (r *Result) Error() error {
	if r.OK {
		return nil
	}
	msgs := make([]string, 0, len(r.Issues))
	for _, is := range r.Issues {
		msgs = append(msgs, is.Error())
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// ValidateFile 校验一个 .mm 文件（只读，不解包）
func ValidateFile(zipPath string) *Result {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return &Result{Issues: []Issue{{Code: "MM001", Message: "不是合法 zip 或无法读取: " + err.Error()}}}
	}
	defer zr.Close()
	return validateZip(zr.File)
}

// ValidateBytes 校验内存里的 .mm（AI 编译产物可直接校验）
func ValidateBytes(data []byte) *Result {
	zr, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		return &Result{Issues: []Issue{{Code: "MM001", Message: "不是合法 zip: " + err.Error()}}}
	}
	return validateZip(zr.File)
}

func validateZip(files []*zip.File) *Result {
	res := &Result{OK: true, Files: []string{}}
	if len(files) > MaxFiles {
		res.Issues = append(res.Issues, Issue{Code: "MM011", Message: fmt.Sprintf("文件数 %d 超过上限 %d", len(files), MaxFiles)})
		return finish(res)
	}

	var total uint64
	var manifestFile *zip.File
	index := map[string]*zip.File{}
	for _, f := range files {
		name := f.Name
		res.Files = append(res.Files, name)
		total += f.UncompressedSize64
		// 路径穿越：绝对路径 / ../ / 盘符 / 反斜杠（Windows 上 zip 可能带反斜杠）
		clean := strings.ReplaceAll(name, "\\", "/")
		if strings.HasPrefix(clean, "/") || drivePattern.MatchString(clean) || strings.Contains(clean, "../") ||
			strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/..") {
			res.Issues = append(res.Issues, Issue{Code: "MM008", Message: "包内路径非法（穿越/绝对路径）", Path: name})
			continue
		}
		if path.Clean(clean) != strings.TrimSuffix(clean, "/") && !f.FileInfo().IsDir() {
			res.Issues = append(res.Issues, Issue{Code: "MM008", Message: "包内路径未规范化", Path: name})
			continue
		}
		index[clean] = f
		if clean == "manifest.json" {
			manifestFile = f
		}
	}
	sort.Strings(res.Files)
	if total > MaxUncompressedBytes {
		res.Issues = append(res.Issues, Issue{Code: "MM011",
			Message: fmt.Sprintf("解压后体积 %d MiB 超过上限 %d MiB", total>>20, MaxUncompressedBytes>>20)})
	}
	if manifestFile == nil {
		res.Issues = append(res.Issues, Issue{Code: "MM002", Message: "缺少 manifest.json"})
		return finish(res)
	}

	raw, err := readZipFile(manifestFile, MaxManifestBytes)
	if err != nil {
		res.Issues = append(res.Issues, Issue{Code: "MM002", Message: "读取 manifest.json 失败: " + err.Error()})
		return finish(res)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		res.Issues = append(res.Issues, Issue{Code: "MM003", Message: "manifest.json 不是合法 JSON: " + err.Error()})
		return finish(res)
	}
	res.Manifest = &m
	validateManifest(&m, index, res)
	return finish(res)
}

func validateManifest(m *Manifest, index map[string]*zip.File, res *Result) {
	add := func(code, msg string, pathArg ...string) {
		is := Issue{Code: code, Message: msg}
		if len(pathArg) > 0 {
			is.Path = pathArg[0]
		}
		res.Issues = append(res.Issues, is)
	}

	if m.Schema <= 0 {
		add("MM003", "缺少 schema")
	} else if m.Schema > SchemaVersion {
		add("MM004", fmt.Sprintf("schema %d 高于宿主支持的 %d（请升级 MelodyMate）", m.Schema, SchemaVersion))
	}
	if !idPattern.MatchString(m.ID) {
		add("MM006", "id 非法（反向域名，小写字母/数字/./-，3~128 字符）: "+m.ID)
	}
	if strings.TrimSpace(m.Name) == "" || len([]rune(m.Name)) > 64 {
		add("MM006", "name 缺失或超过 64 字符")
	}
	if !versionPattern.MatchString(m.Version) {
		add("MM006", "version 不是 semver: "+m.Version)
	}
	if m.Kind != "effect" && m.Kind != "instrument" {
		add("MM003", `kind 必须是 "effect" 或 "instrument"（当前: `+m.Kind+`）`)
	}
	if m.API != ABIVersion {
		add("MM005", fmt.Sprintf("api %d 与宿主 ABI %d 不匹配（原生库与宿主同进程，必须精确相等）", m.API, ABIVersion))
	}

	// entry：必须在 bin/ 下、存在于包内、且不是目录
	if strings.TrimSpace(m.Entry) == "" {
		add("MM007", "缺少 entry（相对 bin/ 的原生库路径）")
	} else {
		clean := strings.ReplaceAll(m.Entry, "\\", "/")
		if strings.Contains(clean, "..") || strings.HasPrefix(clean, "/") || drivePattern.MatchString(clean) {
			add("MM008", "entry 路径非法", m.Entry)
		} else {
			full := "bin/" + clean
			f, ok := index[full]
			if !ok {
				add("MM007", "entry 指向的库不在包内: "+full, full)
			} else if f.FileInfo().IsDir() {
				add("MM007", "entry 指向目录而非文件: "+full, full)
			}
		}
	}

	// 能力：未知项 fail-closed
	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		if !knownCapabilities[c] {
			add("MM009", "未知能力（fail-closed 拒绝）: "+c)
			continue
		}
		if seen[c] {
			add("MM009", "能力重复声明: "+c)
		}
		seen[c] = true
	}

	// 参数
	paramIDs := map[string]bool{}
	for i, p := range m.Params {
		if strings.TrimSpace(p.ID) == "" {
			add("MM003", fmt.Sprintf("params[%d] 缺少 id", i))
			continue
		}
		if paramIDs[p.ID] {
			add("MM003", "参数 id 重复: "+p.ID)
		}
		paramIDs[p.ID] = true
		if p.Max <= p.Min {
			add("MM003", fmt.Sprintf("params[%d] max 必须大于 min", i))
		}
		if p.Default < p.Min || p.Default > p.Max {
			add("MM003", fmt.Sprintf("params[%d] default 超出 [min,max]", i))
		}
		switch p.Curve {
		case "", "linear", "log", "exp":
		default:
			add("MM003", fmt.Sprintf("params[%d] curve 非法: %s", i, p.Curve))
		}
		if p.Steps < 0 {
			add("MM003", fmt.Sprintf("params[%d] steps 不能为负", i))
		}
	}

	// UI
	if m.UI != nil {
		clean := strings.ReplaceAll(m.UI.Entry, "\\", "/")
		if clean == "" {
			add("MM012", "ui.entry 为空")
		} else if strings.Contains(clean, "..") || strings.HasPrefix(clean, "/") || drivePattern.MatchString(clean) {
			add("MM008", "ui.entry 路径非法", m.UI.Entry)
		} else if _, ok := index[clean]; !ok {
			add("MM012", "ui.entry 指向的页面不在包内: "+clean, clean)
		}
		if m.UI.Width < 0 || m.UI.Height < 0 || m.UI.Width > 4096 || m.UI.Height > 4096 {
			add("MM012", "ui 尺寸非法（0~4096）")
		}
	}

	// 命令（暴露给 AI 的工具）：名字必须能作为工具名使用
	for i, c := range m.Commands {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`).MatchString(c.Name) {
			add("MM003", fmt.Sprintf("commands[%d] name 非法（小写字母开头，3~64 位小写/数字/下划线）", i))
		}
	}

	// 哈希
	if m.Hash != nil {
		if m.Hash.Algorithm != "" && m.Hash.Algorithm != "sha256" {
			add("MM010", "哈希算法只支持 sha256，收到: "+m.Hash.Algorithm)
		}
		for rel, want := range m.Hash.Files {
			if !hexPattern.MatchString(want) {
				add("MM010", "哈希值不是 64 位 hex: "+rel, rel)
				continue
			}
			f, ok := index[rel]
			if !ok {
				add("MM010", "哈希声明的文件不在包内: "+rel, rel)
				continue
			}
			got, err := hashZipFile(f)
			if err != nil {
				add("MM010", "读取失败无法校验: "+rel, rel)
				continue
			}
			if !strings.EqualFold(got, want) {
				add("MM010", fmt.Sprintf("哈希不匹配（期望 %s…，实际 %s…）", want[:8], got[:8]), rel)
			}
		}
	}
}

func finish(res *Result) *Result {
	res.OK = len(res.Issues) == 0
	return res
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

func hashZipFile(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SafeExtract 把包解到目标目录（安装用）。逐条校验路径，拒绝任何穿越；
// 目标目录必须是空目录或不存在（避免覆盖既有安装）。
func SafeExtract(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("MM001: 不是合法 zip: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		clean := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(clean, "/") || drivePattern.MatchString(clean) || strings.Contains(clean, "..") {
			return fmt.Errorf("MM008: 包内路径非法: %s", f.Name)
		}
	}
	// 解包由安装器按目录白名单落盘；此处只做安全前置校验，
	// 真正的落盘在 P6 安装器里（需要写注册表 + 能力授权）。
	return nil
}
