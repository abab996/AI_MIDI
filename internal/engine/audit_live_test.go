//go:build windows

package engine

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"melodymate/internal/plugins"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in: starts and terminates only the process owned by this supervisor.
func TestAuditLiveRestartAndProjectClear(t *testing.T) {
	exe := os.Getenv("MM_AUDIT_ENGINE")
	if exe == "" {
		t.Skip("set MM_AUDIT_ENGINE for native integration")
	}
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugins")
	os.MkdirAll(pluginDir, 0700)
	paths := map[string]string{}
	for name, env := range map[string]string{"synth": "MM_AUDIT_SYNTH", "gain": "MM_AUDIT_GAIN"} {
		raw, err := os.ReadFile(os.Getenv(env))
		if err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(pluginDir, name+".dll")
		if err = os.WriteFile(paths[name], raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sup := NewSupervisor(Config{PluginsDir: pluginDir, SoundFontDir: filepath.Join(root, "fonts"), Dirs: func() (string, string, []string) { return root, root, nil }}, AudioSettings{EngineEnabled: true, EnginePath: exe})
	sup.pluginStates = plugins.NewCache()
	sup.ctx, sup.cancel = context.WithCancel(context.Background())
	sup.doneCh = make(chan struct{})
	go sup.loop()
	defer sup.Stop()
	waitEngineState(t, sup, StateReady, 35*time.Second)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(sup.BeginPluginSession("P", 1, []string{"stable-track"}))
	_, err := sup.LoadMmPlugin(0, -1, paths["synth"])
	must(err)
	sup.SetPluginReference(0, -1, "synth@1")
	_, err = sup.LoadMmPlugin(-1, 0, paths["gain"])
	must(err)
	sup.SetPluginReference(-1, 0, "gain@1")
	must(sup.SetPluginParam(-1, 0, 0, .8))
	must(sup.SetFxChain(-1, []FxSlotParams{{Slot: 0, Plugin: "gain@1", Wet: .25, Bypass: false}}))
	must(sup.SetMaster(0))
	state, err := sup.PluginGetState(-1, 0)
	must(err)
	payload, err := base64.StdEncoding.DecodeString(state)
	must(err)
	sup.pluginStates.Put(plugins.Target{ProjectID: "P", Track: -1, Slot: 0}, "gain@1", "mm", payload)
	sup.mu.RLock()
	owned := sup.cmd.Process
	oldPID := owned.Pid
	sup.mu.RUnlock()
	must(owned.Kill())
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		status := sup.Status()
		if status.State == StateReady && status.PID != oldPID && status.Restarts > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	status := sup.Status()
	if status.State != StateReady || status.PID == oldPID || len(status.ReplayErrors) > 0 {
		t.Fatalf("restart failed: %+v", status)
	}
	params, err := sup.PluginParams(-1, 0)
	must(err)
	if got := params["params"].([]any)[0].(map[string]any)["value"].(float64); got < .799 || got > .801 {
		t.Fatalf("state not replayed: %v", got)
	}
	cli, err := sup.Ready(5 * time.Second)
	must(err)
	response, err := cli.Request("fx.getChain", map[string]any{"master": true}, 5*time.Second)
	must(err)
	must(response.Err())
	raw := response.Result
	var chain map[string]any
	must(json.Unmarshal(raw, &chain))
	firstSlot := chain["slots"].([]any)[0].(map[string]any)
	if firstSlot["bypass"] != false || firstSlot["wet"] != .25 {
		t.Fatalf("FX parameters lost: %s", raw)
	}
	t.Logf("replayed master FX: %s", raw)
	out := filepath.Join(root, "zero.wav")
	_, err = sup.Bounce(map[string]any{"path": out, "bpm": 150, "beats": .5, "tailSec": .1, "sampleRate": 48000, "notes": []map[string]any{{"track": 0, "key": 60, "vel": 100, "start": 0, "end": .25}}})
	must(err)
	wav, err := os.ReadFile(out)
	must(err)
	found := false
	for offset := 12; offset+8 <= len(wav); {
		size := int(binary.LittleEndian.Uint32(wav[offset+4:]))
		if offset+8+size > len(wav) {
			t.Fatal("bad WAV")
		}
		if string(wav[offset:offset+4]) == "data" {
			found = true
			for _, b := range wav[offset+8 : offset+8+size] {
				if b != 0 {
					t.Fatal("master gain 0 was not restored")
				}
			}
			break
		}
		offset += 8 + size + (size % 2)
	}
	if !found {
		t.Fatal("WAV data absent")
	}
	must(sup.BeginPluginSession("empty", 2, nil))
	instances, err := sup.PluginState()
	must(err)
	if len(instances["instruments"].([]any)) != 0 || len(instances["effects"].([]any)) != 0 {
		t.Fatalf("empty project retained plugins: %#v", instances)
	}
	if _, err := sup.ResolvePluginTrack("P", "stable-track", 1); err == nil {
		t.Fatal("old project session accepted")
	}
	t.Log("real process restart: instances, FX, state, gain 0, empty switch and stale target passed")
}

// S-M1 异步 SF2 加载：真实引擎上验证状态机（running→done）与快照更新。
// 需要 MM_AUDIT_ENGINE 与 MM_AUDIT_SF2（大音色文件）。
func TestAuditLiveSoundFontAsyncLoad(t *testing.T) {
	exe := os.Getenv("MM_AUDIT_ENGINE")
	sf2 := os.Getenv("MM_AUDIT_SF2")
	if exe == "" || sf2 == "" {
		t.Skip("set MM_AUDIT_ENGINE and MM_AUDIT_SF2 for native integration")
	}
	root := t.TempDir()
	sup := NewSupervisor(Config{
		SoundFontDir: filepath.Dir(sf2),
		Dirs:         func() (string, string, []string) { return filepath.Dir(sf2), root, nil },
	}, AudioSettings{EngineEnabled: true, EnginePath: exe})
	sup.pluginStates = plugins.NewCache()
	sup.ctx, sup.cancel = context.WithCancel(context.Background())
	sup.doneCh = make(chan struct{})
	go sup.loop()
	defer sup.Stop()
	waitEngineState(t, sup, StateReady, 35*time.Second)

	if err := sup.StartSoundFontLoad(0, sf2); err != nil {
		t.Fatal(err)
	}
	var final SoundFontLoadState
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		final = sup.SoundFontLoadStatus(0)
		if !final.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Running {
		t.Fatalf("load did not finish: %+v", final)
	}
	if final.Phase != "done" {
		t.Fatalf("load failed: %+v", final)
	}
	if final.ElapsedMS < 0 {
		t.Fatalf("invalid elapsed: %+v", final)
	}
	sup.mu.RLock()
	path := sup.lastSoundFonts[0]
	sup.mu.RUnlock()
	if path != sf2 {
		t.Fatalf("snapshot not updated: %q", path)
	}

	// 取消路径：立即取消，允许"已完成"或"已取消"两种终态，但不能卡住。
	if err := sup.StartSoundFontLoad(0, sf2); err != nil {
		t.Fatal(err)
	}
	_ = sup.CancelSoundFontLoad(0)
	deadline = time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		final = sup.SoundFontLoadStatus(0)
		if !final.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Running {
		t.Fatalf("cancelled load did not finish: %+v", final)
	}
	if final.Phase != "cancelled" && final.Phase != "done" {
		t.Fatalf("unexpected cancel result: %+v", final)
	}
	t.Logf("async SF2 load: phase=%s elapsed=%dms size=%d", final.Phase, final.ElapsedMS, final.Size)
}

// P-H2 中间插入/移轨：BeginPluginSession 不应 plugin.clear，而是把插件
// 从旧索引卸载、按新索引重装；未移动的实例原地保留。
func TestAuditLiveTrackRemapMovesPlugins(t *testing.T) {
	exe := os.Getenv("MM_AUDIT_ENGINE")
	synth := os.Getenv("MM_AUDIT_SYNTH")
	gain := os.Getenv("MM_AUDIT_GAIN")
	if exe == "" || synth == "" || gain == "" {
		t.Skip("set MM_AUDIT_ENGINE / MM_AUDIT_SYNTH / MM_AUDIT_GAIN")
	}
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for name, src := range map[string]string{"synth": synth, "gain": gain} {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(pluginDir, name+".dll")
		if err := os.WriteFile(paths[name], raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sup := NewSupervisor(Config{
		PluginsDir:   pluginDir,
		SoundFontDir: filepath.Join(root, "fonts"),
		Dirs:         func() (string, string, []string) { return filepath.Join(root, "fonts"), root, nil },
	}, AudioSettings{EngineEnabled: true, EnginePath: exe})
	sup.pluginStates = plugins.NewCache()
	sup.ctx, sup.cancel = context.WithCancel(context.Background())
	sup.doneCh = make(chan struct{})
	go sup.loop()
	defer sup.Stop()
	waitEngineState(t, sup, StateReady, 35*time.Second)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(sup.BeginPluginSession("P", 1, []string{"a", "b", "c"}))
	_, err := sup.LoadMmPlugin(0, -1, paths["synth"])
	must(err)
	sup.SetPluginReference(0, -1, "synth-a")
	_, err = sup.LoadMmPlugin(1, -1, paths["synth"])
	must(err)
	sup.SetPluginReference(1, -1, "synth-b")
	_, err = sup.LoadMmPlugin(0, 0, paths["gain"])
	must(err)
	sup.SetPluginReference(0, 0, "gain-a")

	// 前端插入一条新轨到最前：a 0→1，b 1→2，c 2→3（FX 随 a 移到 1）
	must(sup.BeginPluginSession("P", 1, []string{"x", "a", "b", "c"}))
	if idx, err := sup.ResolvePluginTrack("P", "a", 1); err != nil || idx != 1 {
		t.Fatalf("a should map to 1, got %d err=%v", idx, err)
	}
	sup.mu.RLock()
	aInst := sup.loadedPlugins[pluginKey(1, -1)]
	bInst := sup.loadedPlugins[pluginKey(2, -1)]
	aFx := sup.loadedPlugins[pluginKey(1, 0)]
	_, xHasPlugin := sup.loadedPlugins[pluginKey(0, -1)]
	sup.mu.RUnlock()
	if aInst.Ref != "synth-a" || bInst.Ref != "synth-b" || aFx.Ref != "gain-a" {
		t.Fatalf("plugins not remapped: a=%+v b=%+v fx=%+v", aInst, bInst, aFx)
	}
	if xHasPlugin {
		t.Fatal("new track x must not inherit a plugin at index 0")
	}

	// 再把 b 移到最前：b 2→0，a 1→2，x 0→1（索引未变的 c 3 保持）
	must(sup.BeginPluginSession("P", 1, []string{"b", "x", "a", "c"}))
	sup.mu.RLock()
	bInst = sup.loadedPlugins[pluginKey(0, -1)]
	aInst = sup.loadedPlugins[pluginKey(2, -1)]
	aFx = sup.loadedPlugins[pluginKey(2, 0)]
	sup.mu.RUnlock()
	if bInst.Ref != "synth-b" || aInst.Ref != "synth-a" || aFx.Ref != "gain-a" {
		t.Fatalf("reorder failed: b=%+v a=%+v fx=%+v", bInst, aInst, aFx)
	}
	t.Log("middle insert + reorder remapped plugins without full clear")
}

// P-M6 进程外扫描：子进程扫描 → 返回清单 → 运行中引擎刷新缓存后可装载。
func TestAuditLiveScanPluginsExternal(t *testing.T) {
	exe := os.Getenv("MM_AUDIT_ENGINE")
	vst3Dir := os.Getenv("MM_AUDIT_VST3")
	if exe == "" || vst3Dir == "" {
		t.Skip("set MM_AUDIT_ENGINE and MM_AUDIT_VST3")
	}
	root := t.TempDir()
	fonts := filepath.Join(root, "fonts")
	sup := NewSupervisor(Config{
		SoundFontDir: fonts,
		Dirs:         func() (string, string, []string) { return fonts, root, nil },
	}, AudioSettings{EngineEnabled: true, EnginePath: exe})
	sup.pluginStates = plugins.NewCache()
	sup.ctx, sup.cancel = context.WithCancel(context.Background())
	sup.doneCh = make(chan struct{})
	go sup.loop()
	defer sup.Stop()
	waitEngineState(t, sup, StateReady, 35*time.Second)

	list, err := sup.ScanPlugins([]string{vst3Dir}, 3*time.Minute)
	if err != nil {
		t.Fatalf("external scan failed: %v", err)
	}
	if list.Count == 0 || len(list.Plugins) == 0 {
		t.Fatalf("scan returned no plugins: %+v", list)
	}
	var instrument, effect string
	for _, p := range list.Plugins {
		if p.Instrument && instrument == "" {
			instrument = p.ID
		}
		if !p.Instrument && effect == "" {
			effect = p.ID
		}
	}
	loaded := false
	if instrument != "" {
		if err := sup.LoadPlugin(0, -1, instrument); err == nil {
			loaded = true
			_ = sup.UnloadPlugin(0, -1)
		} else {
			t.Logf("instrument load after external scan failed: %v", err)
		}
	}
	if !loaded && effect != "" {
		if err := sup.LoadPlugin(0, 0, effect); err == nil {
			loaded = true
			_ = sup.UnloadPlugin(0, 0)
		} else {
			t.Logf("effect load after external scan failed: %v", err)
		}
	}
	if !loaded {
		t.Fatal("scanned plugin could not be loaded after live engine cache refresh")
	}
	t.Logf("external scan: %d plugins; live engine cache refreshed and plugin loaded", list.Count)
}
