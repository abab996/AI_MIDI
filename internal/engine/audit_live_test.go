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
