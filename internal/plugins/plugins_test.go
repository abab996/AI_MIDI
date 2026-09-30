package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetFileNameAndValid(t *testing.T) {
	cases := []struct {
		name string
		t    Target
		file string
		ok   bool
	}{
		{"轨 FX 槽", Target{ProjectID: "p1", Track: 3, Slot: 0}, "track_3_slot_0.state", true},
		{"主链 FX 槽", Target{ProjectID: "p1", Track: -1, Slot: 7}, "master_slot_7.state", true},
		{"轨乐器", Target{ProjectID: "p1", Track: 31, Slot: -1}, "track_31_inst.state", true},
		{"缺工程 ID", Target{Track: 0, Slot: 0}, "", false},
		{"轨道越界", Target{ProjectID: "p1", Track: 32, Slot: 0}, "", false},
		{"槽位越界", Target{ProjectID: "p1", Track: 0, Slot: 8}, "", false},
		{"主链无槽位", Target{ProjectID: "p1", Track: -1, Slot: -1}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.t.Valid()
			if c.ok && err != nil {
				t.Fatalf("期望合法，得到错误 %v", err)
			}
			if !c.ok {
				if err == nil {
					t.Fatalf("期望非法，却通过了校验")
				}
				return
			}
			if got := c.t.FileName(); got != c.file {
				t.Fatalf("文件名 = %q，期望 %q", got, c.file)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	target := Target{ProjectID: "proj", Track: 2, Slot: 3}
	payload := []byte{0x01, 0x02, 0x00, 0xff, 0x7f}

	if err := store.Save(target, "VST3|Serum", "vst3", payload); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	// 落在 <工程目录>/plugins/ 下，且不残留临时文件
	if _, err := os.Stat(filepath.Join(dir, "plugins", "track_2_slot_3.state")); err != nil {
		t.Fatalf("状态文件未落在预期位置: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "track_2_slot_3.state.tmp")); !os.IsNotExist(err) {
		t.Fatalf("临时文件未清理")
	}

	env, err := store.Load(target)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if env.Version != envelopeVersion {
		t.Fatalf("版本 = %d，期望 %d", env.Version, envelopeVersion)
	}
	if env.Ref != "VST3|Serum" || env.Format != "vst3" {
		t.Fatalf("引用/格式 = %q/%q", env.Ref, env.Format)
	}
	if env.Bytes != len(payload) {
		t.Fatalf("Bytes = %d，期望 %d", env.Bytes, len(payload))
	}
	got, err := env.Payload()
	if err != nil {
		t.Fatalf("Payload 解码失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("负载不一致：%v", got)
	}
	if env.SavedAt <= 0 {
		t.Fatalf("SavedAt 未写入")
	}
}

func TestSaveRejectsOversizeAndEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	target := Target{ProjectID: "p", Track: 0, Slot: 0}

	if err := store.Save(target, "x", "vst3", nil); err == nil {
		t.Fatalf("空状态应当被拒绝")
	}
	big := make([]byte, MaxStateBytes+1)
	if err := store.Save(target, "x", "vst3", big); err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("超限状态应当被拒绝，得到 %v", err)
	}
	// 边界：恰好等于上限应当允许
	if err := store.Save(target, "x", "vst3", make([]byte, MaxStateBytes)); err != nil {
		t.Fatalf("恰好等于上限应当允许，得到 %v", err)
	}
}

func TestSaveRejectsBadTarget(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Save(Target{ProjectID: "p", Track: 99, Slot: 0}, "x", "vst3", []byte{1}); err == nil {
		t.Fatalf("越界轨道应当被拒绝")
	}
}

func TestLoadCorruptAndWrongVersion(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	target := Target{ProjectID: "p", Track: 1, Slot: 1}
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugins", target.FileName())

	if err := os.WriteFile(path, []byte("{不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(target); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("损坏文件应当报错，得到 %v", err)
	}

	env := Envelope{Version: 99, Data: "AQ=="}
	raw, _ := json.Marshal(env)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(target); err == nil || !strings.Contains(err.Error(), "版本不支持") {
		t.Fatalf("版本不符应当报错，得到 %v", err)
	}

	env = Envelope{Version: envelopeVersion, Data: "!!!不是 base64"}
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(target); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("非法负载应当报错，得到 %v", err)
	}
}

func TestDeleteAndList(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	// 目录不存在时 List 返回空而不是报错
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("空目录 List = %v, %v", list, err)
	}

	targets := []Target{
		{ProjectID: "p", Track: 0, Slot: 0},
		{ProjectID: "p", Track: -1, Slot: 2},
		{ProjectID: "p", Track: 5, Slot: -1},
	}
	for _, tg := range targets {
		if err := store.Save(tg, "ref", "mm", []byte{7}); err != nil {
			t.Fatalf("Save(%v) 失败: %v", tg, err)
		}
	}
	// 干扰文件：不该出现在 List 里
	if err := os.WriteFile(filepath.Join(dir, "plugins", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List 返回 %d 项，期望 3：%v", len(list), list)
	}
	if list[0].FileName() != "master_slot_2.state" || list[2].FileName() != "track_5_inst.state" {
		t.Fatalf("List 排序/解析不符：%v", list)
	}

	if err := store.Delete(targets[0]); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if err := store.Delete(targets[0]); err != nil {
		t.Fatalf("重复删除应当幂等：%v", err)
	}
	if list, _ := store.List(); len(list) != 2 {
		t.Fatalf("删除后 List = %d 项，期望 2", len(list))
	}
}

func TestEnvelopeMatches(t *testing.T) {
	env := Envelope{Ref: "VST3|Krush"}
	if !env.Matches("VST3|Krush") {
		t.Fatalf("同引用应当匹配")
	}
	if env.Matches("VST3|Serum") {
		t.Fatalf("不同引用必须不匹配（否则会把 A 的状态灌进 B）")
	}
	if !env.Matches("") {
		t.Fatalf("调用方不校验引用时应当放行")
	}
	if !(Envelope{}).Matches("VST3|Serum") {
		t.Fatalf("老状态文件没有引用时应当放行而不是拒绝恢复")
	}
}

func TestCacheActiveProjectIsolation(t *testing.T) {
	c := NewCache()
	a := Target{ProjectID: "A", Track: 0, Slot: 0}
	b := Target{ProjectID: "B", Track: 0, Slot: 0}
	c.Put(a, "VST3|Krush", "vst3", []byte{1})
	c.Put(b, "VST3|Serum", "vst3", []byte{2})

	if c.ActiveProject() != "B" {
		t.Fatalf("最近写入的工程应当成为当前工程，得到 %q", c.ActiveProject())
	}
	states := c.ActiveStates()
	if len(states) != 1 || states[0].Ref != "VST3|Serum" {
		t.Fatalf("当前工程状态过滤错误：%v", states)
	}

	c.SetActiveProject("A")
	states = c.ActiveStates()
	if len(states) != 1 || states[0].Ref != "VST3|Krush" {
		t.Fatalf("切工程后状态过滤错误：%v", states)
	}

	c.Drop(a)
	if states := c.ActiveStates(); len(states) != 0 {
		t.Fatalf("Drop 后不应残留：%v", states)
	}

	// 超限状态不入缓存（避免内存被单份巨物拖垮）
	c.Put(Target{ProjectID: "A", Track: 1, Slot: 1}, "x", "vst3", make([]byte, MaxStateBytes+1))
	if len(c.ActiveStates()) != 0 {
		t.Fatalf("超限状态不应进缓存")
	}

	// nil 缓存不 panic（调用方可能没注入）
	var nilCache *Cache
	nilCache.Put(a, "x", "vst3", []byte{1})
	nilCache.Drop(a)
	nilCache.SetActiveProject("A")
	if nilCache.ActiveProject() != "" || nilCache.ActiveStates() != nil {
		t.Fatalf("nil 缓存应当安全空转")
	}
}
