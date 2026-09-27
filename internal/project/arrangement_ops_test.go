package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"melodymate/internal/config"
	"melodymate/internal/soundlib"
)

// setupArrangementEnv 把数据根/项目目录/注册表一并换绑到临时目录，
// 返回临时根与一个已建好的项目 id。
func setupArrangementEnv(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib, oldSettings, oldProjects := config.ProjectRoot, config.LibraryDir, config.SettingsFile, config.ProjectsDir
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.SettingsFile = filepath.Join(root, "settings.json")
	config.ProjectsDir = filepath.Join(root, "projects")
	// 索引是包级内存镜像：切目录后必须清空，否则旧目录的条目会被写进
	// 新目录的 index.json，污染同包其它用例（见 ResetProjectsIndexCache 注释）
	ResetProjectsIndexCache()
	t.Cleanup(func() {
		config.ProjectRoot, config.LibraryDir = oldRoot, oldLib
		config.SettingsFile, config.ProjectsDir = oldSettings, oldProjects
		ResetProjectsIndexCache()
	})

	meta, err := CreateProject("编排测试项目")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return root, meta.ID
}

// registerFakeSF2 在注册表里登记一个 SF2 条目（不要求文件真实存在：
// 机架解析用 id/名称，不做音频解码）
func registerFakeSF2(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "Library", "soundfonts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir soundfonts: %v", err)
	}
	abs := filepath.Join(dir, name+".sf2")
	if err := os.WriteFile(abs, []byte("not-a-real-sf2"), 0o644); err != nil {
		t.Fatalf("write sf2: %v", err)
	}
	src, _, err := soundlib.EnsureSF2Source(abs, name)
	if err != nil {
		t.Fatalf("EnsureSF2Source: %v", err)
	}
	return src.ID
}

func writeV1Arrangement(t *testing.T, projectID string, tracks []any) {
	t.Helper()
	doc := map[string]any{
		"version": 1,
		"bpm":     120,
		"snap":    0.25,
		"ppb":     26,
		"loop":    map[string]any{"on": false, "start": 0.0, "end": 16.0},
		"tracks":  tracks,
	}
	raw, _ := json.Marshal(doc)
	if err := WriteArrangement(projectID, raw); err != nil {
		t.Fatalf("WriteArrangement: %v", err)
	}
}

func readArrangementMap(t *testing.T, projectID string) map[string]any {
	t.Helper()
	raw, err := ReadArrangement(projectID)
	if err != nil || raw == nil {
		t.Fatalf("ReadArrangement: %v (nil=%v)", err, raw == nil)
	}
	var arr map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("unmarshal arrangement: %v", err)
	}
	return arr
}

func rackItemsOf(t *testing.T, arr map[string]any) []map[string]any {
	t.Helper()
	rack, _ := arr["rack"].(map[string]any)
	if rack == nil {
		t.Fatalf("arrangement 缺少 rack 字段: %v", arr)
	}
	raw, _ := rack["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("机架条目不是对象: %v", it)
		}
		out = append(out, m)
	}
	return out
}

func trackSourceOf(t *testing.T, arr map[string]any, idx int) map[string]any {
	t.Helper()
	tracks, _ := arr["tracks"].([]any)
	if idx >= len(tracks) {
		t.Fatalf("轨道 %d 不存在（共 %d 条）", idx, len(tracks))
	}
	tm, _ := tracks[idx].(map[string]any)
	src, _ := tm["source"].(map[string]any)
	if src == nil {
		t.Fatalf("轨道 %d 缺少 source", idx)
	}
	return src
}

// ── v1 → v2 迁移 ─────────────────────────────────────────────

func TestMigrateV1ArrangementBuildsRack(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	guID := registerFakeSF2(t, root, "GeneralUser GS")
	sampleID := registerFakeSF2(t, root, "PianoTest")

	writeV1Arrangement(t, pid, []any{
		map[string]any{
			"id": "tr_a", "name": "Track 1", "color": "#00B8CC", "volume": 0.8,
			"source": map[string]any{"type": "synth", "wave": "square", "label": "合成器 · 方波"},
			"clips":  []any{},
		},
		map[string]any{
			"id": "tr_b", "name": "Track 2", "color": "#FF8C00", "volume": 0.7,
			"source": map[string]any{"type": "builtin", "tone": "strings", "label": "内置 · 弦乐群"},
			"clips":  []any{},
		},
		map[string]any{
			"id": "tr_c", "name": "Track 3", "color": "#06D6A0", "volume": 0.6,
			"source": map[string]any{"type": "sf2", "sourceId": sampleID, "presetId": "0:0", "label": "钢琴"},
			"clips":  []any{},
		},
	})

	res, err := ApplyOps(pid, []map[string]any{
		{"op": "addTrack", "name": "新轨", "itemId": BuiltinSynthSourceID, "presetId": "triangle"},
	})
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	arr := res.Arrangement

	items := rackItemsOf(t, arr)
	if len(items) != 4 {
		t.Fatalf("机架条目数 = %d, want 4（synth square / strings / PianoTest / synth triangle）: %v", len(items), items)
	}

	// 轨道 source 全部换成机架引用
	for i := 0; i < 4; i++ {
		src := trackSourceOf(t, arr, i)
		if strings.TrimSpace(fmt.Sprint(src["itemId"])) == "" {
			t.Fatalf("轨道 %d 未迁移为 itemId 引用: %v", i, src)
		}
	}

	// builtin strings → GeneralUser GS 0:48（注册表里有 GeneralUser 时优先它）
	src1 := trackSourceOf(t, arr, 1)
	item1 := findItemByID(t, arr, fmt.Sprint(src1["itemId"]))
	if item1["sourceId"] != guID {
		t.Fatalf("builtin strings 应映射到 GeneralUser（%s），实际 %v", guID, item1["sourceId"])
	}
	if item1["presetId"] != "0:48" {
		t.Fatalf("弦乐预设应为 0:48，实际 %v", item1["presetId"])
	}

	// synth square 保留波形
	src0 := trackSourceOf(t, arr, 0)
	item0 := findItemByID(t, arr, fmt.Sprint(src0["itemId"]))
	if item0["kind"] != RackKindSynth || item0["presetId"] != "square" {
		t.Fatalf("合成器条目 = %v, want kind=synth preset=square", item0)
	}

	// 版本升到 2，且落盘
	if fmt.Sprint(arr["version"]) != "2" {
		t.Fatalf("version = %v, want 2", arr["version"])
	}
	persisted := readArrangementMap(t, pid)
	if fmt.Sprint(persisted["version"]) != "2" {
		t.Fatalf("落盘 version = %v, want 2", persisted["version"])
	}
	if len(rackItemsOf(t, persisted)) != 4 {
		t.Fatalf("落盘机架条目数 = %d, want 4", len(rackItemsOf(t, persisted)))
	}
}

func TestMigrateWithoutGeneralUserFallsBackToBuiltinGM(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	writeV1Arrangement(t, pid, []any{
		map[string]any{
			"id": "tr_a", "name": "Track 1", "color": "#00B8CC", "volume": 0.8,
			"source": map[string]any{"type": "builtin", "tone": "piano"},
			"clips":  []any{},
		},
	})
	arr := LoadArrangementV2(pid)
	src := trackSourceOf(t, arr, 0)
	item := findItemByID(t, arr, fmt.Sprint(src["itemId"]))
	if item["sourceId"] != BuiltinGMSourceID || item["presetId"] != "0:0" {
		t.Fatalf("无 GeneralUser 时应回落 builtin:gm 0:0，实际 %v", item)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	registerFakeSF2(t, root, "GeneralUser GS")
	writeV1Arrangement(t, pid, []any{
		map[string]any{"id": "tr_a", "name": "A", "volume": 0.8, "source": map[string]any{"type": "synth", "wave": "sine"}, "clips": []any{}},
	})
	first := LoadArrangementV2(pid)
	second := LoadArrangementV2(pid)
	if mustJSON(t, first) != mustJSON(t, second) {
		t.Fatalf("迁移不幂等:\n%s\n%s", mustJSON(t, first), mustJSON(t, second))
	}
	if len(rackItemsOf(t, second)) != 1 {
		t.Fatalf("重复迁移产生了重复条目: %v", rackItemsOf(t, second))
	}
}

func TestOpsPreserveUnknownFields(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	doc := map[string]any{
		"version": 2,
		"bpm":     128,
		"tracks": []any{
			map[string]any{
				"id": "tr_a", "name": "钢琴", "color": "#00B8CC", "volume": 0.8,
				"source": map[string]any{"itemId": "rk_x", "presetId": "0:0"},
				"clips": []any{
					map[string]any{
						"id": "clip_1", "type": "midi", "name": "melody", "start": 0.0, "length": 4.0,
						"notes": []any{map[string]any{"note": "C4", "velocity": 100, "start": 0.0, "end": 1.0}},
					},
				},
			},
		},
		"rack":      map[string]any{"items": []any{map[string]any{"id": "rk_x", "kind": "synth", "sourceId": BuiltinSynthSourceID, "name": "合成器"}}},
		"myVendor":  map[string]any{"future": true},
		"transport": "custom",
	}
	raw, _ := json.Marshal(doc)
	if err := WriteArrangement(pid, raw); err != nil {
		t.Fatalf("WriteArrangement: %v", err)
	}

	res, err := ApplyOps(pid, []map[string]any{
		{"op": "setTrackMix", "track": 0, "gain": 0.5, "pan": -0.25},
	})
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}

	if _, ok := res.Arrangement["myVendor"]; !ok {
		t.Fatalf("未知字段 myVendor 被丢弃: %v", res.Arrangement)
	}
	if res.Arrangement["transport"] != "custom" {
		t.Fatalf("未知字段 transport 被改写: %v", res.Arrangement["transport"])
	}
	tracks, _ := res.Arrangement["tracks"].([]any)
	tm, _ := tracks[0].(map[string]any)
	clips, _ := tm["clips"].([]any)
	if len(clips) != 1 {
		t.Fatalf("clips 丢失: %v", tm["clips"])
	}
	clip, _ := clips[0].(map[string]any)
	notes, _ := clip["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("clip.notes 丢失: %v", clip)
	}
	if fmt.Sprint(tm["volume"]) != "0.5" {
		t.Fatalf("volume = %v, want 0.5", tm["volume"])
	}
	mixer, _ := res.Arrangement["mixer"].(map[string]any)
	channels, _ := mixer["channels"].(map[string]any)
	ch, _ := channels["tr_a"].(map[string]any)
	if ch == nil || fmt.Sprint(ch["pan"]) != "-0.25" {
		t.Fatalf("mixer.channels 未写入 pan: %v", channels)
	}
}

// ── 校验 ─────────────────────────────────────────────────────

func TestOpsValidation(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	sf2ID := registerFakeSF2(t, root, "GeneralUser GS")
	if _, err := ApplyOps(pid, []map[string]any{
		{"op": "addRackItem", "sourceId": sf2ID, "presetId": "0:0"},
	}); err != nil {
		t.Fatalf("addRackItem: %v", err)
	}
	arr := LoadArrangementV2(pid)
	itemID := fmt.Sprint(rackItemsOf(t, arr)[0]["id"])

	cases := []struct {
		name string
		ops  []map[string]any
		want string
	}{
		{"未知操作", []map[string]any{{"op": "explode"}}, "未知操作"},
		{"缺少 op", []map[string]any{{}}, "缺少 op"},
		{"空 ops", []map[string]any{}, "不能为空"},
		{"音源不存在", []map[string]any{{"op": "addRackItem", "sourceId": "nope"}}, "音源不存在"},
		{"轨道越界", []map[string]any{{"op": "setTrackSource", "track": 99, "itemId": itemID}}, "超出范围"},
		{"音源不在机架", []map[string]any{{"op": "setTrackSource", "track": 0, "itemId": "rk_nope"}}, "不在机架"},
		{"gain 越界", []map[string]any{{"op": "setTrackMix", "track": 0, "gain": 1.5}}, "gain 超出范围"},
		{"pan 越界", []map[string]any{{"op": "setTrackMix", "track": 0, "pan": -9}}, "pan 超出范围"},
		{"混音空参", []map[string]any{{"op": "setTrackMix", "track": 0}}, "至少要给出"},
		{"bpm 越界", []map[string]any{{"op": "setBpm", "bpm": 999}}, "bpm 超出范围"},
		{"循环反向", []map[string]any{{"op": "setLoop", "start": 8, "end": 4}}, "循环终点必须大于起点"},
		{"改名空", []map[string]any{{"op": "renameTrack", "track": 0, "name": "  "}}, "name 不能为空"},
		{"类型错误", []map[string]any{{"op": "setTrackMix", "track": 0, "mute": "yes"}}, "必须是布尔值"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ApplyOps(pid, tc.ops)
			if err == nil {
				t.Fatalf("期望报错（%s），实际成功", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息 = %q，期望包含 %q", err.Error(), tc.want)
			}
		})
	}

	// 校验失败不得落盘（rev 不推进，机架不变）
	before := mustJSON(t, readArrangementMap(t, pid))
	if _, err := ApplyOps(pid, []map[string]any{{"op": "setBpm", "bpm": 999}}); err == nil {
		t.Fatal("非法 bpm 应被拒绝")
	}
	if after := mustJSON(t, readArrangementMap(t, pid)); after != before {
		t.Fatalf("校验失败仍然改写了编排数据")
	}
}

func TestRemoveRackItemInUse(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	sf2ID := registerFakeSF2(t, root, "GeneralUser GS")
	res, err := ApplyOps(pid, []map[string]any{
		{"op": "addRackItem", "sourceId": sf2ID, "presetId": "0:0"},
	})
	if err != nil {
		t.Fatalf("addRackItem: %v", err)
	}
	itemID := fmt.Sprint(rackItemsOf(t, res.Arrangement)[0]["id"])

	// 默认轨道经迁移指向首个条目；即便没有，也先显式指派一次
	if _, err := ApplyOps(pid, []map[string]any{
		{"op": "setTrackSource", "track": 0, "itemId": itemID},
	}); err != nil {
		t.Fatalf("setTrackSource: %v", err)
	}

	_, err = ApplyOps(pid, []map[string]any{{"op": "removeRackItem", "itemId": itemID}})
	if err == nil || !strings.Contains(err.Error(), "仍被") {
		t.Fatalf("被占用的条目应拒绝删除，实际 err=%v", err)
	}

	res, err = ApplyOps(pid, []map[string]any{{"op": "removeRackItem", "itemId": itemID, "force": true}})
	if err != nil {
		t.Fatalf("force 删除失败: %v", err)
	}
	if len(rackItemsOf(t, res.Arrangement)) != 1 {
		t.Fatalf("force 删除后应回落补一个内置条目，实际 %v", rackItemsOf(t, res.Arrangement))
	}
	src := trackSourceOf(t, res.Arrangement, 0)
	if src["itemId"] == itemID {
		t.Fatalf("轨道仍指向已删条目: %v", src)
	}
}

func TestOpsDedupeRackItem(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	sf2ID := registerFakeSF2(t, root, "GeneralUser GS")
	for i := 0; i < 3; i++ {
		if _, err := ApplyOps(pid, []map[string]any{
			{"op": "addRackItem", "sourceId": sf2ID, "presetId": "0:48"},
		}); err != nil {
			t.Fatalf("addRackItem #%d: %v", i, err)
		}
	}
	n := 0
	for _, it := range rackItemsOf(t, LoadArrangementV2(pid)) {
		if it["sourceId"] == sf2ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一音源重复入架应复用条目，实际 %d 条", n)
	}
}

func TestRackItemLimit(t *testing.T) {
	root, pid := setupArrangementEnv(t)
	// 造足够多的不同采样包条目绕开去重，验证上限
	dir := filepath.Join(root, "samples")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hitLimit := false
	for i := 0; i < RackMaxItems+5; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("pack%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		src, err := soundlib.Add(soundlib.Source{
			Kind: soundlib.KindSamplePack, Name: fmt.Sprintf("包%02d", i), Dirs: []string{sub}, Enabled: true,
		})
		if err != nil {
			t.Fatalf("soundlib.Add: %v", err)
		}
		if _, err := ApplyOps(pid, []map[string]any{{"op": "addRackItem", "sourceId": src.ID}}); err != nil {
			if !strings.Contains(err.Error(), "上限") {
				t.Fatalf("addRackItem 失败原因异常: %v", err)
			}
			hitLimit = true
			break
		}
	}
	if !hitLimit {
		t.Fatalf("超过 %d 个条目仍未报上限", RackMaxItems)
	}
	if n := len(rackItemsOf(t, LoadArrangementV2(pid))); n != RackMaxItems {
		t.Fatalf("机架条目数 = %d, want %d", n, RackMaxItems)
	}
}

// ── 并发与 rev ───────────────────────────────────────────────

func TestOpsConcurrentNoLostUpdate(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	const n = 24

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := ApplyOps(pid, []map[string]any{
				{"op": "addTrack", "name": fmt.Sprintf("并发轨 %d", i)},
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 ApplyOps 失败: %v", err)
	}

	arr := LoadArrangementV2(pid)
	tracks, _ := arr["tracks"].([]any)
	if len(tracks) != n+1 { // 初始 1 条 + n 条
		t.Fatalf("轨道数 = %d, want %d（并发丢更新）", len(tracks), n+1)
	}
	if rev := intField(arr, "rev"); rev != n {
		t.Fatalf("rev = %d, want %d", rev, n)
	}
}

func TestWriteArrangementVersionedBumpsRev(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	if _, err := ApplyOps(pid, []map[string]any{{"op": "setBpm", "bpm": 100}}); err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	arr := LoadArrangementV2(pid)
	rev1 := intField(arr, "rev")

	raw, _ := json.Marshal(arr)
	rev2, err := WriteArrangementVersioned(pid, raw)
	if err != nil {
		t.Fatalf("WriteArrangementVersioned: %v", err)
	}
	if rev2 <= rev1 {
		t.Fatalf("整份写入后 rev 应单调递增: %d → %d", rev1, rev2)
	}
	persisted := readArrangementMap(t, pid)
	if intField(persisted, "rev") != rev2 {
		t.Fatalf("落盘 rev = %v, want %d", persisted["rev"], rev2)
	}
	if fmt.Sprint(persisted["bpm"]) != "100" {
		t.Fatalf("整份写入应保留内容，bpm = %v", persisted["bpm"])
	}

	if _, err := WriteArrangementVersioned(pid, []byte("{ 坏 json")); err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
}

func TestApplyOpsPianoRoll(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	res, err := ApplyOps(pid, []map[string]any{
		{"op": "addRackItem", "sourceId": BuiltinSynthSourceID, "presetId": "sine"},
	})
	if err != nil {
		t.Fatalf("addRackItem: %v", err)
	}
	itemID := fmt.Sprint(rackItemsOf(t, res.Arrangement)[0]["id"])

	res, err = ApplyOps(pid, []map[string]any{
		{"op": "setPianoRoll", "itemId": itemID, "presetId": "sine"},
	})
	if err != nil {
		t.Fatalf("setPianoRoll: %v", err)
	}
	pr, _ := res.Arrangement["pianoRoll"].(map[string]any)
	if pr == nil || pr["itemId"] != itemID {
		t.Fatalf("pianoRoll 未写入: %v", res.Arrangement["pianoRoll"])
	}
	if len(res.Changed) != 1 || res.Changed[0] != ChangePianoRoll {
		t.Fatalf("changed = %v, want [pianoRoll]", res.Changed)
	}
}

// ── 小工具 ───────────────────────────────────────────────────

// TestRackItemIDVectors 条目 id 的固定向量：与前端
// frontend/src/arrangement/rack.ts 的 rackItemId 必须逐字一致
// （同一张表也在 tools/dev/rack_check.mjs 里断言）。两端不一致会出现
// "AI 加一条、界面加一条"的重复机架条目。
func TestRackItemIDVectors(t *testing.T) {
	cases := []struct {
		kind, sourceID, presetID, path, want string
	}{
		{"synth", "builtin:synth", "sawtooth", "", "rk_3c069be59463"},
		{"synth", "builtin:synth", "square", "", "rk_f51a10c53900"},
		{"sf2", "builtin:gm", "0:0", "", "rk_2e9a99d4602d"},
		{"sf2", "sf2_generaluser_ab12cd", "0:48", "", "rk_8bd68c9021c3"},
		{"sf2", "sf2_pianolib_x9", "0:0", "", "rk_48ca2ed690d1"},
		{"mm", "mm_ai_stereo_77", "", "", "rk_607a9773c699"},
		{"sample", "D:/Samples/Kick 808.wav", "", "D:/samples/kick 808.wav", "rk_64f8b156d5f8"},
	}
	for _, c := range cases {
		if got := rackItemID(c.kind, c.sourceID, c.presetID, c.path); got != c.want {
			t.Errorf("rackItemID(%q,%q,%q,%q) = %q, want %q",
				c.kind, c.sourceID, c.presetID, c.path, got, c.want)
		}
	}
	// 大小写不敏感（Windows 语义）：音源 id/路径的大小写变化不应产生新条目
	if rackItemID("sf2", "SF2_X", "0:0", "") != rackItemID("sf2", "sf2_x", "0:0", "") {
		t.Error("条目 id 应对大小写不敏感")
	}
}

func findItemByID(t *testing.T, arr map[string]any, id string) map[string]any {
	t.Helper()
	for _, it := range rackItemsOf(t, arr) {
		if fmt.Sprint(it["id"]) == id {
			return it
		}
	}
	t.Fatalf("机架里找不到条目 %s：%v", id, rackItemsOf(t, arr))
	return nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
