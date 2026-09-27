package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LegacyMigration 是从 AI_MIDI 数据目录复制到 MelodyMate 数据根的一次结果。
// 复制不删除原目录，便于回滚。Announced 为 true 表示前端已经提示过。
type LegacyMigration struct {
	From       string `json:"from"`
	Files      int    `json:"files"`
	SoundFonts int    `json:"soundfonts"`
	Announced  bool   `json:"announced"`
}

const migrationFileName = "melodymate-migration.json"

var migrationMu sync.Mutex

// DefaultLegacyRoots 返回可能仍存有 AI_MIDI 用户数据的目录。
// 安装器 per-user 目录在先，便携布局的同级 AI_MIDI 目录在后。
func DefaultLegacyRoots(newRoot string) []string {
	var roots []string
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		roots = append(roots, filepath.Join(la, "Programs", "AI_MIDI"))
	}
	if newRoot != "" {
		roots = append(roots, filepath.Join(filepath.Dir(newRoot), "AI_MIDI"))
	}
	return roots
}

// MigrateLegacyInstall 在新数据根还没有 settings.json、也没有迁移记录时，
// 从旧目录复制 settings.json、projects、Library/user、Library/soundfonts、output。
// 已存在的目标文件不覆盖。没有可迁移数据时返回零值。
func MigrateLegacyInstall(newRoot string) (LegacyMigration, error) {
	return MigrateLegacyData(newRoot, DefaultLegacyRoots(newRoot))
}

// MigrateLegacyData 是可测试的迁移入口。legacyRoots 按顺序使用第一个含数据的目录。
func MigrateLegacyData(newRoot string, legacyRoots []string) (LegacyMigration, error) {
	newRoot = filepath.Clean(newRoot)
	if newRoot == "" || newRoot == "." {
		return LegacyMigration{}, nil
	}
	if fileExists(filepath.Join(newRoot, "settings.json")) || fileExists(migrationPath(newRoot)) {
		return LegacyMigration{}, nil
	}

	var src string
	for _, root := range legacyRoots {
		root = filepath.Clean(root)
		if root == "" || strings.EqualFold(root, newRoot) {
			continue
		}
		if legacyHasData(root) {
			src = root
			break
		}
	}
	if src == "" {
		return LegacyMigration{}, nil
	}

	var result LegacyMigration
	result.From = src
	for _, dir := range []string{
		"projects",
		filepath.Join("Library", "user"),
		filepath.Join("Library", "soundfonts"),
		"output",
	} {
		from := filepath.Join(src, dir)
		info, err := os.Stat(from)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := copyTree(from, filepath.Join(newRoot, dir), &result); err != nil {
			return result, err
		}
	}
	if fileExists(filepath.Join(src, "settings.json")) {
		copied, err := copyNewFile(filepath.Join(src, "settings.json"), filepath.Join(newRoot, "settings.json"))
		if err != nil {
			return result, err
		}
		if copied {
			result.Files++
		}
	}
	if result.Files == 0 {
		return LegacyMigration{}, nil
	}
	if err := writeMigration(newRoot, result); err != nil {
		return result, err
	}
	return result, nil
}

// TakeMigrationAnnouncement 读出尚未提示的迁移结果，并把它标成已提示。
// 没有待提示记录时 ok 为 false。
func TakeMigrationAnnouncement(newRoot string) (LegacyMigration, bool) {
	migrationMu.Lock()
	defer migrationMu.Unlock()

	path := migrationPath(newRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return LegacyMigration{}, false
	}
	var result LegacyMigration
	if err := json.Unmarshal(data, &result); err != nil || result.Announced || result.Files == 0 {
		return LegacyMigration{}, false
	}
	result.Announced = true
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return LegacyMigration{}, false
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return LegacyMigration{}, false
	}
	return result, true
}

func migrationPath(newRoot string) string {
	return filepath.Join(newRoot, "output", migrationFileName)
}

func writeMigration(newRoot string, result LegacyMigration) error {
	if err := os.MkdirAll(filepath.Join(newRoot, "output"), 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(migrationPath(newRoot), raw, 0644)
}

func legacyHasData(root string) bool {
	if fileExists(filepath.Join(root, "settings.json")) {
		return true
	}
	for _, dir := range []string{"projects", filepath.Join("Library", "user"), filepath.Join("Library", "soundfonts")} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func copyTree(src, dst string, result *LegacyMigration) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		copied, err := copyNewFile(path, target)
		if err != nil || !copied {
			return err
		}
		result.Files++
		if strings.EqualFold(filepath.Ext(path), ".sf2") {
			result.SoundFonts++
		}
		return nil
	})
}

func copyNewFile(src, dst string) (bool, error) {
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return false, copyErr
	}
	return true, closeErr
}
