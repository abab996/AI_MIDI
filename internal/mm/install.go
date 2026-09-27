package mm

// .mm 包安装（P6）：校验 → 能力授权门禁 → 安全解包。
//
// 放在 internal/mm 而不是只在 cmd/mm 里：主程序（设置页/HTTP/AI 工具）与命令行
// 工具必须走**同一套**门禁，否则会出现"命令行拒绝、界面放行"的缺口。
//
// 授权语义（fail-closed）：
//   · 插件声明而未授权的能力 → 拒绝安装（返回缺失清单，供界面弹确认框）；
//   · 授予了插件未声明的能力 → 也拒绝（不允许多授权，避免权限悄悄扩大）；
//   · 声明为空（纯 DSP 插件）→ 无需授权即可安装。

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InstallPreview 安装前预览（不落盘）：界面据此弹能力确认框
type InstallPreview struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Vendor       string   `json:"vendor"`
	Kind         string   `json:"kind"`
	Capabilities []string `json:"capabilities"`
	HashVerified bool     `json:"hashVerified"`
	AI           bool     `json:"ai"`
	TargetDir    string   `json:"targetDir"`
	MissingGrant []string `json:"missingGrant"` // 声明了但未授权的能力
}

// ValidateAndPreview PreviewInstall 的对外名字（server/AI 工具用）
func ValidateAndPreview(zipPath, destRoot string, granted []string) (*InstallPreview, error) {
	return PreviewInstall(zipPath, destRoot, granted)
}

// PreviewInstall 校验包并给出安装预览（不写盘）
func PreviewInstall(zipPath, destRoot string, granted []string) (*InstallPreview, error) {
	res := ValidateFile(zipPath)
	if !res.OK || res.Manifest == nil {
		return nil, res.Error()
	}
	m := res.Manifest
	grant := map[string]bool{}
	for _, g := range granted {
		grant[strings.TrimSpace(g)] = true
	}
	declared := map[string]bool{}
	var missing []string
	for _, c := range m.Capabilities {
		declared[c] = true
		if !grant[c] {
			missing = append(missing, c)
		}
	}
	for g := range grant {
		if g == "" {
			continue
		}
		if !declared[g] {
			return nil, fmt.Errorf("授权了插件未声明的能力 %q（不允许多授权）", g)
		}
	}
	sort.Strings(missing)
	return &InstallPreview{
		ID:           m.ID,
		Name:         m.Name,
		Version:      m.Version,
		Vendor:       m.Vendor,
		Kind:         m.Kind,
		Capabilities: append([]string(nil), m.Capabilities...),
		HashVerified: m.Hash != nil && len(m.Hash.Files) > 0,
		AI:           m.IsAI(),
		TargetDir:    filepath.Join(destRoot, fmt.Sprintf("%s@%s", m.ID, m.Version)),
		MissingGrant: missing,
	}, nil
}

// Install 安装 .mm 到 destRoot/<id>@<version>/。granted 为**用户已确认**的能力清单。
// 返回预览信息（含目标目录）；能力未授权时返回错误且**不落盘**。
func Install(zipPath, destRoot string, granted []string) (*InstallPreview, error) {
	preview, err := PreviewInstall(zipPath, destRoot, granted)
	if err != nil {
		return nil, err
	}
	if len(preview.MissingGrant) > 0 {
		return preview, fmt.Errorf("插件声明了能力 [%s] 但尚未授权：请在界面确认能力清单后用 --grant/%s 安装",
			strings.Join(preview.MissingGrant, ", "), strings.Join(preview.MissingGrant, ","))
	}
	if _, statErr := os.Stat(preview.TargetDir); statErr == nil {
		return preview, fmt.Errorf("目标已存在: %s（先卸载或改版本号）", preview.TargetDir)
	}
	if err := extractZip(zipPath, preview.TargetDir); err != nil {
		_ = os.RemoveAll(preview.TargetDir) // 失败不留半成品
		return preview, err
	}
	return preview, nil
}

// Uninstall 删除已安装目录（注册表条目由调用方清理）
func Uninstall(installDir string) error {
	clean := filepath.Clean(installDir)
	if strings.Contains(clean, "..") || !strings.Contains(filepath.ToSlash(clean), "/plugins/") {
		return fmt.Errorf("拒绝删除非插件目录: %s", installDir)
	}
	return os.RemoveAll(clean)
}

// extractZip 逐条路径校验后解包（zip-slip 防护：绝对路径 / .. / 盘符）
func extractZip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("MM001: 不是合法 zip: %w", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		clean := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") || drivePattern.MatchString(clean) {
			return fmt.Errorf("MM008: 包内路径非法: %s", f.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if rel, rerr := filepath.Rel(dest, target); rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("MM008: 解包越界: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, oerr := f.Open()
		if oerr != nil {
			return oerr
		}
		out, cerr := os.Create(target)
		if cerr != nil {
			rc.Close()
			return cerr
		}
		if _, werr := io.Copy(out, rc); werr != nil {
			out.Close()
			rc.Close()
			return werr
		}
		out.Close()
		rc.Close()
	}
	return nil
}
