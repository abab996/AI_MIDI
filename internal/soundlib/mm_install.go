package soundlib

// MM 插件安装 → 音源注册表同步（P6 / 规范 §8）
//
// 为什么放在 soundlib：注册表是它的领域，且 internal/server 与 internal/mcp
// 都要用它（安装后同步、设置页「刷新」按钮、已安装清单）。同步之后 kind=mm
// 的条目会出现在既有的「声音管理」与素材库「音源」页签里（含能力清单、哈希、
// ✨AI 徽标）——不再需要用户在命令行里记装了什么。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"melodymate/internal/config"
)

// mmManifest 同步只需要这些字段；**完整校验归 internal/mm**（.mm 包的
// schema/能力/哈希规则都以那边为准，这里不重复实现）。
type mmManifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Vendor       string   `json:"vendor"`
	Kind         string   `json:"kind"`
	Capabilities []string `json:"capabilities"`
	Author       *struct {
		Type string `json:"type"`
	} `json:"author"`
	UI *struct {
		Entry  string `json:"entry"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"ui"`
	Hash *struct {
		Algorithm string            `json:"algorithm"`
		Files     map[string]string `json:"files"`
	} `json:"hash"`
}

// readMMManifest 读已安装目录里的 manifest.json
func readMMManifest(installDir string) (mmManifest, error) {
	var mf mmManifest
	raw, err := os.ReadFile(filepath.Join(installDir, "manifest.json"))
	if err != nil {
		return mf, err
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		return mf, err
	}
	return mf, nil
}

// mmRelPath 安装目录相对数据根的路径（数据根之外保留绝对路径，
// 开发期与测试直接用临时目录时不会算错——**不依赖进程 CWD**）
func mmRelPath(installDir string) string {
	base := config.LibraryDir
	if base != "" {
		if rel, err := filepath.Rel(base, installDir); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(installDir)
}

// mmHashOf 取一个代表性哈希（注册表只存一个字符串）
func mmHashOf(mf mmManifest) string {
	if mf.Hash == nil {
		return ""
	}
	for _, v := range mf.Hash.Files {
		return v
	}
	return ""
}

func mmIsAI(mf mmManifest) bool {
	return mf.Author != nil && mf.Author.Type == "ai"
}

func mmAIInfo(isAI bool) *AIInfo {
	if !isAI {
		return nil
	}
	return &AIInfo{CreatedBy: "mm-plugin"}
}

// SyncInstalledMM 把一个已安装的 MM 插件目录同步进注册表。
// 已存在同路径条目则更新（重装/升级不产生重复项），返回条目与是否新建。
func SyncInstalledMM(installDir string) (Source, bool, error) {
	mf, err := readMMManifest(installDir)
	if err != nil {
		return Source{}, false, err
	}
	rel := mmRelPath(installDir)
	origin := "user"
	if mmIsAI(mf) {
		origin = "ai"
	}
	caps := append([]string(nil), mf.Capabilities...)
	entryID := strings.ReplaceAll(mf.ID, ".", "_") + "@" + mf.Version

	/* 判重按**确定性 ID**（id@version），不按 Path：normalizeSource 会改写 Path
	   （入库形状与调用方给的不同），按路径比会漏判，随后 Add 直接报"条目已存在" */
	if _, gerr := Get(entryID); gerr == nil {
		src, err := Update(entryID, func(s *Source) error {
			s.Name, s.Vendor = mf.Name, mf.Vendor
			s.Kind, s.Origin = KindMM, origin
			s.Capabilities, s.Hash = caps, mmHashOf(mf)
			s.Category, s.Enabled = mf.Kind, true
			s.ClassUID = mf.ID // 插件 id（带点的反向域名）：界面据此打开 mm-ui.html
			s.UpdatedAt = time.Now().Unix()
			s.AI = mmAIInfo(mmIsAI(mf))
			return nil
		})
		return src, false, err
	}

	src, err := Add(Source{
		ID:           entryID,
		Kind:         KindMM,
		Name:         mf.Name,
		Vendor:       mf.Vendor,
		Origin:       origin,
		Path:         rel,
		Category:     mf.Kind, // effect / instrument
		ClassUID:     mf.ID,   // 插件 id（mm-ui.html?plugin=… 用）
		Capabilities: caps,
		Hash:         mmHashOf(mf),
		Enabled:      true,
		AI:           mmAIInfo(mmIsAI(mf)),
		AddedAt:      time.Now().Unix(),
	})
	if err != nil {
		return Source{}, false, err
	}
	return src, true, nil
}

// SyncAllInstalledMM 扫描 Library/plugins/ 下所有已安装插件并同步，
// 返回 (新增, 更新, 失败) 计数。设置页「刷新」与 /api/engine/plugins/sync 用它。
func SyncAllInstalledMM() (int, int, int) {
	root := filepath.Join(config.LibraryDir, "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, 0
	}
	added, updated, failed := 0, 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, created, err := SyncInstalledMM(filepath.Join(root, e.Name())); err != nil {
			failed++
		} else if created {
			added++
		} else {
			updated++
		}
	}
	return added, updated, failed
}

// UninstallMMSource 卸载插件后清掉注册表条目（避免"幽灵条目"）
func UninstallMMSource(pluginID string) error {
	prefix := strings.ReplaceAll(pluginID, ".", "_") + "@"
	for _, v := range List(KindMM, "") {
		if strings.HasPrefix(v.ID, prefix) {
			if _, err := Delete(v.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// InstalledMMInfo 已安装 MM 插件的摘要（设置页「声音管理·MM 插件」分组用）
func InstalledMMInfo(installDir string) (map[string]any, error) {
	mf, err := readMMManifest(installDir)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":           mf.ID,
		"name":         mf.Name,
		"version":      mf.Version,
		"vendor":       mf.Vendor,
		"kind":         mf.Kind,
		"capabilities": mf.Capabilities,
		"hashVerified": mf.Hash != nil && len(mf.Hash.Files) > 0,
		"uiWidth":      mmUIWidth(mf),
		"uiHeight":     mmUIHeight(mf),
		"ai":           mmIsAI(mf),
		"dir":          filepath.ToSlash(installDir),
	}, nil
}

// ListInstalledMM 全部已安装 MM 插件摘要
func ListInstalledMM() []map[string]any {
	root := filepath.Join(config.LibraryDir, "plugins")
	entries, err := os.ReadDir(root)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if info, err := InstalledMMInfo(filepath.Join(root, e.Name())); err == nil {
			out = append(out, info)
		}
	}
	return out
}

// mmUIWidth / mmUIHeight：界面声明的尺寸（0 表示未声明 → 前端用默认窗口尺寸）
func mmUIWidth(mf mmManifest) int {
	if mf.UI == nil || mf.UI.Width < 0 {
		return 0
	}
	return mf.UI.Width
}

func mmUIHeight(mf mmManifest) int {
	if mf.UI == nil || mf.UI.Height < 0 {
		return 0
	}
	return mf.UI.Height
}
