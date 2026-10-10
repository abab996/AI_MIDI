// Package plugins 负责"插件二进制状态随工程保存"的落盘与进程内缓存。
//
// 背景（计划 fond-shoal-wren.md §3.2.2）：arrangement.json 必须保持小而可读，
// 而 VST3 / MM 插件的二进制状态可达数 MB，因此不进 JSON，按目标单独落文件：
//
//	<工程目录>/plugins/track_<n>_slot_<m>.state   FX 槽（轨道 n 的第 m 槽）
//	<工程目录>/plugins/master_slot_<m>.state      主总线 FX 槽
//	<工程目录>/plugins/track_<n>_inst.state       轨道乐器（VST3 / MM）
//
// 文件内容是一层自描述信封（JSON + base64 负载），装载前可以校验"这份状态
// 是不是当前这个插件的"——把 A 插件的状态灌进 B 插件，轻则报错、重则崩溃，
// 因此引用不一致时宁可拒绝恢复也不硬塞。
//
// 进程内 Cache 解决另一个场景：引擎崩溃重启后 supervisor 会重放 FX 链把
// 插件装回去，但插件自己的参数/状态回到了默认值。把最近一次状态留在内存里，
// 重放完成后立刻推回去，用户感知不到"崩溃后音色变了"。
package plugins

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrReferenceMismatch = errors.New("状态插件引用或格式不匹配")
var storeWriteMu sync.Mutex

func referenceArchive(path, ref, format string) string {
	return path + fmt.Sprintf(".ref-%x", sha256.Sum256([]byte(format+"|"+ref)))
}

const (
	// MaxStateBytes 单份状态上限 5MB（与计划 §3.2.2 的 5MB 口径一致）。
	// 超过即拒绝保存：宁可报错也不能让一个工程目录被状态文件撑爆。
	MaxStateBytes = 5 << 20

	// MaxTracks / MaxSlots 与引擎的 kMaxTracks / kMaxFxSlots 对齐，
	// 用于拒绝越界目标（否则可以构造出任意文件名的写入）。
	MaxTracks = 32
	MaxSlots  = 8

	// envelopeVersion 信封版本（将来改格式时用来拒绝旧文件，而不是误读）。
	envelopeVersion = 1
)

// Target 一个插件状态目标：工程内的一处插件位置。
//   - Track = -1 表示主总线（此时 Slot 必须 >= 0）
//   - Slot  = -1 表示轨道乐器（该轨的 VST3 / MM 乐器）
type Target struct {
	ProjectID string
	TrackID   string
	Track     int
	Slot      int
}

// Valid 校验目标的取值范围。
func (t Target) Valid() error {
	if strings.TrimSpace(t.ProjectID) == "" {
		return fmt.Errorf("缺少工程 ID")
	}
	if t.Track < -1 || t.Track >= MaxTracks {
		return fmt.Errorf("轨道序号超出范围（0 ~ %d，主总线用 -1）", MaxTracks-1)
	}
	if t.Slot < -1 || t.Slot >= MaxSlots {
		return fmt.Errorf("槽位序号超出范围（0 ~ %d，乐器用 -1）", MaxSlots-1)
	}
	if t.Track < 0 && t.Slot < 0 {
		return fmt.Errorf("主总线必须指定 FX 槽位")
	}
	return nil
}

// FileName 目标对应的状态文件名（不含目录）。
func (t Target) FileName() string {
	if t.Track >= 0 && t.TrackID != "" {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(t.TrackID)))
		if t.Slot < 0 {
			return "track_" + hash + "_inst.state"
		}
		return fmt.Sprintf("track_%s_slot_%d.state", hash, t.Slot)
	}
	switch {
	case t.Track < 0:
		return fmt.Sprintf("master_slot_%d.state", t.Slot)
	case t.Slot < 0:
		return fmt.Sprintf("track_%d_inst.state", t.Track)
	default:
		return fmt.Sprintf("track_%d_slot_%d.state", t.Track, t.Slot)
	}
}

// Key 缓存键：同一进程可能同时开着多个工程，键必须带工程前缀，
// 否则 A 工程的状态会被推进 B 工程的同名槽位。
func (t Target) Key() string {
	return t.ProjectID + "|" + t.FileName()
}

// Envelope 状态文件内容。
type Envelope struct {
	TrackID string `json:"trackId,omitempty"`
	Track   int    `json:"track"`
	Slot    int    `json:"slot"`
	Version int    `json:"version"`
	Ref     string `json:"ref"`     // 插件引用（arrangement.mixer.fx[].plugin 原值）
	Format  string `json:"format"`  // vst3 | mm（仅用于可读性与诊断）
	SavedAt int64  `json:"savedAt"` // Unix 秒
	Bytes   int    `json:"bytes"`   // 负载解码后的字节数（自检用）
	Data    string `json:"data"`    // base64 负载
}

// Matches 校验信封是否属于给定的插件引用。
// 只有明确匹配的引用才能恢复，缺失引用的旧文件原样保留。
func (e Envelope) Matches(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && e.Ref == ref
}

// Payload 解码负载。Bytes 与 Data 不一致时以 Data 为准（但调用方会先被
// Load 的完整性校验挡掉）。
func (e Envelope) Payload() ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		return nil, fmt.Errorf("状态负载不是合法 base64：%w", err)
	}
	return data, nil
}

// Store 某个工程的状态目录。
type Store struct {
	dir string
}

// NewStore 以工程目录创建状态存储（目录在首次写入时按需创建）。
func NewStore(projectDir string) *Store {
	return &Store{dir: filepath.Join(projectDir, "plugins")}
}

// Dir 状态目录（诊断用）。
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(t Target) (string, error) {
	if err := t.Valid(); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, t.FileName()), nil
}

// Save 原子写入一份状态。超过 MaxStateBytes 直接拒绝。
func (s *Store) Save(t Target, ref, format string, payload []byte) error {
	storeWriteMu.Lock()
	defer storeWriteMu.Unlock()
	path, err := s.path(t)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return fmt.Errorf("插件返回了空状态，未写入")
	}
	if len(payload) > MaxStateBytes {
		return fmt.Errorf("插件状态 %d 字节超过上限 %d 字节", len(payload), MaxStateBytes)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建插件状态目录失败：%w", err)
	}
	if original, readErr := os.ReadFile(path); readErr == nil {
		old, loadErr := loadStateFile(path, t)
		if loadErr != nil {
			return loadErr
		}
		if old.Ref != strings.TrimSpace(ref) || old.Format != strings.TrimSpace(format) {
			backup, backupErr := os.OpenFile(path+".before-reference-change", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if backupErr == nil {
				_, backupErr = backup.Write(original)
				closeErr := backup.Close()
				if backupErr == nil {
					backupErr = closeErr
				}
			}
			if backupErr != nil && !os.IsExist(backupErr) {
				return backupErr
			}
			if err := os.WriteFile(referenceArchive(path, old.Ref, old.Format), original, 0600); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	env := Envelope{
		TrackID: t.TrackID, Track: t.Track, Slot: t.Slot,
		Version: envelopeVersion,
		Ref:     strings.TrimSpace(ref),
		Format:  strings.TrimSpace(format),
		SavedAt: time.Now().Unix(),
		Bytes:   len(payload),
		Data:    base64.StdEncoding.EncodeToString(payload),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	/* 原子写：临时文件与目标同目录（跨盘 rename 不可用），写完即改名。
	   Windows 上 rename 覆盖已存在文件是允许的（os.Rename 走 MoveFileEx）。 */
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入插件状态失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交插件状态失败：%w", err)
	}
	return nil
}

// Load 读取一份状态。文件不存在返回 os.ErrNotExist 包装的错误。
func (s *Store) Load(t Target) (Envelope, error) {
	path, err := s.path(t)
	if err != nil {
		return Envelope{}, err
	}
	return loadStateFile(path, t)
}

func loadStateFile(path string, t Target) (Envelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("状态文件损坏（%s）：%w", t.FileName(), err)
	}
	if env.Version != envelopeVersion {
		return Envelope{}, fmt.Errorf("状态文件版本不支持（%d，期望 %d）", env.Version, envelopeVersion)
	}
	if t.TrackID != "" && env.TrackID != t.TrackID {
		return Envelope{}, fmt.Errorf("状态属于另一轨道或缺少保存时的稳定轨道映射")
	}
	if _, err := env.Payload(); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Delete 删除该目标的状态文件（文件不存在视为成功）。
func (s *Store) Delete(t Target) error {
	path, err := s.path(t)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List 列出该工程已保存状态的目标（按文件名排序，便于稳定输出）。
func (s *Store) List() ([]Target, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []Target{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".state") {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(s.dir, e.Name()))
		var env Envelope
		if json.Unmarshal(raw, &env) == nil && env.TrackID != "" {
			target := Target{TrackID: env.TrackID, Track: env.Track, Slot: env.Slot}
			if target.FileName() == e.Name() {
				out = append(out, target)
				continue
			}
		}
		if t, ok := parseFileName(e.Name()); ok {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName() < out[j].FileName() })
	return out, nil
}

// LoadMatching migrates an index file only when its saved mapping and plugin identity prove ownership.
func (s *Store) LoadMatching(t Target, ref, format string) (Envelope, error) {
	env, err := s.Load(t)
	if os.IsNotExist(err) && t.TrackID != "" {
		legacy := t
		legacy.TrackID = ""
		old, lerr := s.Load(legacy)
		if lerr != nil {
			return Envelope{}, lerr
		}
		if old.TrackID != t.TrackID || !old.Matches(ref) || old.Format != format {
			return Envelope{}, fmt.Errorf("旧状态缺少匹配的保存时轨道映射或插件引用，保留原文件")
		}
		path, _ := s.path(legacy)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return Envelope{}, rerr
		}
		backup := path + ".before-stable-id"
		if _, berr := os.Stat(backup); os.IsNotExist(berr) {
			if berr = os.WriteFile(backup, raw, 0600); berr != nil {
				return Envelope{}, berr
			}
		}
		payload, _ := old.Payload()
		if err = s.Save(t, ref, format, payload); err != nil {
			return Envelope{}, err
		}
		return s.Load(t)
	}
	if err != nil {
		return Envelope{}, err
	}
	if !env.Matches(ref) || env.Format != format {
		path, _ := s.path(t)
		archived, archiveErr := loadStateFile(referenceArchive(path, ref, format), t)
		if archiveErr == nil && archived.Matches(ref) && archived.Format == format {
			return archived, nil
		}
		if archiveErr != nil && !os.IsNotExist(archiveErr) {
			return Envelope{}, archiveErr
		}
		return Envelope{}, ErrReferenceMismatch
	}
	return env, nil
}

// parseFileName 文件名 → 目标（不认识的名字直接忽略，不报错：
// 目录里可能有用户自己放的东西）。
func parseFileName(name string) (Target, bool) {
	switch {
	case strings.HasPrefix(name, "master_slot_"):
		var slot int
		if _, err := fmt.Sscanf(name, "master_slot_%d.state", &slot); err == nil {
			return Target{Track: -1, Slot: slot}, true
		}
	case strings.HasPrefix(name, "track_") && strings.Contains(name, "_inst.state"):
		var track int
		if _, err := fmt.Sscanf(name, "track_%d_inst.state", &track); err == nil {
			return Target{Track: track, Slot: -1}, true
		}
	default:
		var track, slot int
		if _, err := fmt.Sscanf(name, "track_%d_slot_%d.state", &track, &slot); err == nil {
			return Target{Track: track, Slot: slot}, true
		}
	}
	return Target{}, false
}

// maxCachedStates 缓存条目上限：一个工程最多 32 轨 × 8 槽 + 主链 + 乐器，
// 正常远小于此；设上限只为防病态情况吃内存。
const maxCachedStates = 512

// Cache 进程内"最近一次状态"缓存（引擎重启后推回用）。
type Cache struct {
	mu            sync.Mutex
	byKey         map[string]cachedState
	activeProject string
}

type cachedState struct {
	Target  Target
	Ref     string
	Format  string
	Payload []byte
}

// NewCache 创建缓存。
func NewCache() *Cache {
	return &Cache{byKey: make(map[string]cachedState)}
}

// Default 进程级默认缓存：HTTP 层（保存/恢复）与守护器（引擎重启重放）
// 必须共用同一份，否则重放时推回去的是另一份空缓存。
var (
	defaultOnce  sync.Once
	defaultCache *Cache
)

// Default 返回进程级缓存实例（并发安全，懒初始化）。
func Default() *Cache {
	defaultOnce.Do(func() { defaultCache = NewCache() })
	return defaultCache
}

// Put 记录一份状态（同时把该工程标记为"当前工程"）。
func (c *Cache) Put(t Target, ref, format string, payload []byte) {
	if c == nil || len(payload) == 0 || len(payload) > MaxStateBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byKey) >= maxCachedStates {
		// 满了先丢一半（按目标键排序，删最前面的）——正常永远到不了这里
		keys := make([]string, 0, len(c.byKey))
		for k := range c.byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)/2] {
			delete(c.byKey, k)
		}
	}
	c.byKey[t.Key()] = cachedState{Target: t, Ref: ref, Format: format, Payload: append([]byte(nil), payload...)}
	c.activeProject = t.ProjectID
}

// Drop 丢弃一份状态（槽位清空/卸载时调用）。
func (c *Cache) Drop(t Target) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byKey, t.Key())
}

// SetActiveProject 标记当前工程（打开工程时调用）：引擎重启重放只推
// 当前工程的状态，避免把别的工程的槽位状态推进来。
func (c *Cache) SetActiveProject(projectID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeProject = projectID
}

// ActiveProject 当前工程 ID（诊断/测试用）。
func (c *Cache) ActiveProject() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeProject
}

// ActiveStates 当前工程的全部缓存状态（按目标键排序，保证重放顺序稳定）。
func (c *Cache) ActiveStates() []cachedState {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []cachedState{}
	for _, st := range c.byKey {
		if st.Target.ProjectID != c.activeProject {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target.Key() < out[j].Target.Key() })
	return out
}
