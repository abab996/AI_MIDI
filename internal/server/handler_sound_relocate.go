package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"melodymate/internal/soundlib"
)

// S-M5：缺失音色文件重定位。
//
// 背景：注册表 ID 由路径派生，文件被移动/改名后条目进入 Missing 状态，
// 工程仍引用旧 ID，界面只能看到引擎报错。这里提供：
//   - POST /api/sound/relocate {id}            按内容哈希（优先）或原文件名
//     在音色目录中自动查找并改写注册表路径；
//   - POST /api/sound/relocate {id, path}      用户显式指定新路径；
//   - force=true 时允许内容哈希不一致（用户确认后使用另一个文件）。

var errSoundFontNotFound = errors.New("未找到匹配的音色文件")

func (r *Router) handleSoundRelocate(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	id := strings.TrimSpace(in.ID)
	if id == "" || strings.ContainsAny(id, `/\`) {
		writeError(w, http.StatusBadRequest, "无效的音源 id")
		return
	}
	src, err := soundlib.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "未找到音源条目："+id)
		return
	}
	if src.Kind != soundlib.KindSF2 {
		writeError(w, http.StatusBadRequest, "只有 SF2 音色条目支持定位文件")
		return
	}

	oldPath := soundlib.ResolvePath(src.Path)
	explicit := strings.TrimSpace(in.Path) != ""
	var target string
	var hash [32]byte

	if explicit {
		target = filepath.Clean(strings.TrimSpace(in.Path))
		info, serr := os.Stat(target)
		if serr != nil || info.IsDir() {
			writeError(w, http.StatusBadRequest, "指定的音色文件不存在或不是文件："+target)
			return
		}
		if !strings.EqualFold(filepath.Ext(target), ".sf2") {
			writeError(w, http.StatusBadRequest, "定位文件必须是 .sf2 音色库")
			return
		}
		if !sf2MagicOK(target) {
			writeError(w, http.StatusBadRequest, "不是有效的 SF2 文件（RIFF/sfbk 校验失败）")
			return
		}
		hash, err = fileSHA256(target)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取音色文件失败："+err.Error())
			return
		}
		actual := hex.EncodeToString(hash[:])
		if src.Hash != "" && !strings.EqualFold(src.Hash, actual) && !in.Force {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code":     "soundfont_hash_mismatch",
				"error":    "文件内容与注册表记录不一致（可能是另一个音色库）。确认要使用它请重试 force=true",
				"expected": src.Hash,
				"actual":   actual,
				"path":     target,
			})
			return
		}
	} else {
		target, hash, err = locateSoundFont(src, oldPath)
		if errors.Is(err, errSoundFontNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"code":     "soundfont_not_found",
				"error":    "音色目录中没有找到同哈希/同名的 SF2，请手动指定文件路径",
				"searched": soundlib.SoundFontDir(),
			})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "定位音色文件失败："+err.Error())
			return
		}
	}

	updated, uerr := soundlib.Update(id, func(s *soundlib.Source) error {
		s.Path = soundlib.StorePath(target)
		s.Hash = hex.EncodeToString(hash[:])
		return nil
	})
	if uerr != nil {
		writeError(w, http.StatusInternalServerError, "更新音源路径失败："+uerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"source":   updated,
		"previous": src.Path,
		"path":     target,
		"hash":     hex.EncodeToString(hash[:]),
	})
}

// locateSoundFont 在音色目录（以及旧文件所在目录）里查找：
//  1. 有内容哈希时按哈希精确匹配（优先）；
//  2. 否则按原文件名（不区分大小写）匹配。
//
// 多个匹配时优先同名文件，其次按路径排序取第一个，保证结果稳定。
func locateSoundFont(src soundlib.View, oldPath string) (string, [32]byte, error) {
	roots := []string{soundlib.SoundFontDir()}
	if dir := filepath.Dir(oldPath); dir != "" && !strings.EqualFold(dir, roots[0]) {
		roots = append(roots, dir)
	}
	seen := map[string]bool{}
	var byHash []string
	var byName []string
	wantName := strings.ToLower(filepath.Base(oldPath))

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".sf2") || seen[path] || strings.EqualFold(path, oldPath) {
				return nil
			}
			seen[path] = true
			if strings.EqualFold(filepath.Base(path), wantName) {
				byName = append(byName, path)
			}
			if src.Hash != "" {
				if h, herr := fileSHA256(path); herr == nil && strings.EqualFold(hex.EncodeToString(h[:]), src.Hash) {
					byHash = append(byHash, path)
				}
			}
			return nil
		})
		if walkErr != nil {
			return "", [32]byte{}, walkErr
		}
	}

	pick := func(candidates []string) (string, [32]byte, error) {
		if len(candidates) == 0 {
			return "", [32]byte{}, errSoundFontNotFound
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			iName := strings.EqualFold(filepath.Base(candidates[i]), wantName)
			jName := strings.EqualFold(filepath.Base(candidates[j]), wantName)
			if iName != jName {
				return iName
			}
			return candidates[i] < candidates[j]
		})
		target := candidates[0]
		h, herr := fileSHA256(target)
		if herr != nil {
			return "", [32]byte{}, herr
		}
		return target, h, nil
	}

	if len(byHash) > 0 {
		return pick(byHash)
	}
	return pick(byName)
}

// sf2MagicOK 校验 RIFF....sfbk 魔数（只读 12 字节）。
func sf2MagicOK(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var header [12]byte
	if _, err := ioReadFull(f, header[:]); err != nil {
		return false
	}
	return string(header[0:4]) == "RIFF" && string(header[8:12]) == "sfbk"
}

// ioReadFull 小工具：避免为 12 字节头引入额外依赖。
func ioReadFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
