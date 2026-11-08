package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendAudioRouting(t *testing.T) {
	// 验证默认走 JUCE：audio_engine.js 的 isNativePreferred 应被使用
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "arrangement", "audio_engine.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	if !strings.Contains(s, "isNativePreferred") {
		t.Fatal("audio_engine.js should check isNativePreferred for JUCE routing")
	}
	if !strings.Contains(s, "noteOnTrack") {
		t.Fatal("audio_engine.js should call EngineBridge.noteOnTrack for native")
	}
	// 验证 _queueAudioClip 在原生路径（引擎模式）跳过 Web 队列——
	// 严格路由：SamplePool 批量调度失败也不回退 Web 队列（只按
	// isNativePreferred() 早退，不再检查 _nativeSamplesFailed）
	if !strings.Contains(s, "if (isNativePreferred()) return;") {
		t.Fatal("_queueAudioClip should early return when native (strict, no Web fallback)")
	}
	if strings.Contains(s, "if (isNativePreferred() && !this._nativeSamplesFailed) return;") {
		t.Fatal("_queueAudioClip must not fall back to Web queue after sample scheduling failure")
	}
	// synth 轨原生直通需同步引擎侧内置波形声部（不依赖 SF2）
	if !strings.Contains(s, "_ensureTrackWaveVoice") {
		t.Fatal("audio_engine.js should sync engine wave voice for synth tracks")
	}
	// 引擎模式不得强制 WebAudio（_forceWebAudio 降级路径已删除）
	if strings.Contains(s, "_forceWebAudio") {
		t.Fatal("audio_engine.js must not use _forceWebAudio (strict routing, no Web fallback)")
	}
	// 引擎模式节拍器走引擎（click / PREVIEW_TRACK）
	if !strings.Contains(s, "PREVIEW_TRACK") {
		t.Fatal("audio_engine.js should route metronome click to engine preview track")
	}
}

func TestFrontendPianorollRouting(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "pianoroll", "pianoroll.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	if !strings.Contains(s, "isNativePreferred") {
		t.Fatal("pianoroll.js should check isNativePreferred")
	}
	if !strings.Contains(s, "noteOnTrack") {
		t.Fatal("pianoroll.js should use noteOnTrack for native")
	}
	// 播放头回归护栏：钢琴窗本地播放不得跟随引擎 timecode——引擎走带
	// 由编曲窗启动，钢琴窗从不 play，跟随只会冻结在 tc.beat
	if strings.Contains(s, "return tc.beat") {
		t.Fatal("pianoroll.js must not freeze playhead on engine timecode (tc.playing is always false here)")
	}
	// 原生演奏应走专用演奏轨 + 内置波形声部（不依赖 SF2）
	if !strings.Contains(s, "PERF_TRACK") || !strings.Contains(s, "_ensureNativeVoice") {
		t.Fatal("pianoroll.js should route native notes via PERF_TRACK with wave voice sync")
	}
}

func TestFrontendEngineBridge(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "engine", "engine_bridge.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	// 必须有 timecode 和 levels 轮询
	if !strings.Contains(s, "__engineTimecode") {
		t.Fatal("engine_bridge.js should have timecode polling")
	}
	if !strings.Contains(s, "__engineLevels") {
		t.Fatal("engine_bridge.js should have levels polling")
	}
	if !strings.Contains(s, "getLevels") {
		t.Fatal("engine_bridge.js should expose getLevels")
	}
	if !strings.Contains(s, "setLoop") {
		t.Fatal("engine_bridge.js should expose setLoop")
	}
	if !strings.Contains(s, "setTrackVoice") || !strings.Contains(s, "PERF_TRACK") {
		t.Fatal("engine_bridge.js should expose setTrackVoice and PERF_TRACK")
	}
}

func TestFrontendSettingsConsistency(t *testing.T) {
	// ASIO 按钮应与测试音一致，不应有深底硬编码
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "settings.html"))
	if err != nil {
		t.Skip(err.Error())
	}
	if bytes.Contains(data, []byte(`style="background:#2a3b4c`)) {
		t.Fatal("settings.html ASIO button should not have hardcoded dark background")
	}
	css, err := os.ReadFile(filepath.Join("..", "..", "frontend", "style.css"))
	if err == nil {
		// 不应有 #openPanelBtn 的深底覆盖（已统一）
		if bytes.Contains(css, []byte("#openPanelBtn")) && bytes.Contains(css, []byte("#2a3b4c")) {
			t.Fatal("style.css should not have #openPanelBtn dark override after unification")
		}
	}
}

func TestEngineMixerAndTransport(t *testing.T) {
	// 验证 Mixer 有压限，Transport 有循环
	mixerData, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "MixerGraph.h"))
	if err == nil {
		s := string(mixerData)
		if !strings.Contains(s, "Compressor") || !strings.Contains(s, "Limiter") {
			t.Fatal("MixerGraph.h should have Compressor/Limiter")
		}
		if !strings.Contains(s, "peakLevel") {
			t.Fatal("MixerGraph should compute peakLevel")
		}
	}
	transportData, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "Transport.h"))
	if err == nil {
		s := string(transportData)
		if !strings.Contains(s, "setLoop") {
			t.Fatal("Transport.h should have setLoop")
		}
		if !strings.Contains(s, "loopOn_") {
			t.Fatal("Transport should have loop state")
		}
	}
}

// S-L2 回归：换音色必须走短淡出，不能再硬切旧 tsf。
// 引擎无法在 CI 里编译时，这条源码级检查至少保证：
//   - 存在淡出槽与 beginFade/mixFading；
//   - loadSoundFont / setWaveVoice 的替换路径调用 beginFade；
//   - render 把淡出输出混进主输出；
//   - bounce 前清空淡出，避免旧尾巴进入导出。
func TestSoundFontSwitchCrossfade(t *testing.T) {
	header, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "SynthEngine.h"))
	if err != nil {
		t.Skip(err.Error())
	}
	h := string(header)
	for _, needle := range []string{"FadeSlot", "beginFade", "mixFading", "flushFade"} {
		if !strings.Contains(h, needle) {
			t.Fatalf("SynthEngine.h missing %q (S-L2 crossfade)", needle)
		}
	}
	impl, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "SynthEngine.cpp"))
	if err != nil {
		t.Skip(err.Error())
	}
	c := string(impl)
	for _, needle := range []string{
		"void SynthEngine::beginFade",
		"bool SynthEngine::mixFading",
		"beginFade(old)",
		"beginFade(oldTsf)",
		"mixFading(outputChannelData",
	} {
		if !strings.Contains(c, needle) {
			t.Fatalf("SynthEngine.cpp missing %q (S-L2 crossfade)", needle)
		}
	}
	device, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "EngineAudioDevice.cpp"))
	if err == nil && !strings.Contains(string(device), "flushFade()") {
		t.Fatal("bounce must flush pending source-switch fades before rendering")
	}
}

// E-M4 回归：多输出乐器总线必须存在路由表、音频路径与 IPC 入口。
func TestInstrumentBusRoutingWiring(t *testing.T) {
	header, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "EngineAudioDevice.h"))
	if err != nil {
		t.Skip(err.Error())
	}
	h := string(header)
	for _, needle := range []string{"kMaxAuxBuses", "setInstrumentBusRouting", "instrumentBusJson", "routeInstrumentAux", "applyAuxToBuffers"} {
		if !strings.Contains(h, needle) {
			t.Fatalf("EngineAudioDevice.h missing %q (E-M4 bus routing)", needle)
		}
	}
	impl, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "EngineAudioDevice.cpp"))
	if err != nil {
		t.Skip(err.Error())
	}
	c := string(impl)
	for _, needle := range []string{"EngineAudioDevice::setInstrumentBusRouting", "EngineAudioDevice::routeInstrumentAux", "aux.addFrom"} {
		if !strings.Contains(c, needle) {
			t.Fatalf("EngineAudioDevice.cpp missing %q (E-M4 bus routing)", needle)
		}
	}
	mainCpp, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Main.cpp"))
	if err == nil && (!strings.Contains(string(mainCpp), "plugin.setBusRouting") || !strings.Contains(string(mainCpp), "plugin.busInfo")) {
		t.Fatal("Main.cpp must expose plugin.setBusRouting / plugin.busInfo")
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "engine", "Tests", "VstFixture.cpp"))
	if err == nil && !strings.Contains(string(fixture), `withOutput("Aux"`) {
		t.Fatal("AuditSynth fixture should expose a second output bus for E-M4 smoke")
	}
}

// E-M5 回归：VST3 状态读写必须走后台线程池，不能在消息线程同步执行。
func TestPluginStateBackgroundIO(t *testing.T) {
	header, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "EngineAudioDevice.h"))
	if err != nil {
		t.Skip(err.Error())
	}
	h := string(header)
	for _, needle := range []string{"PluginStateCallback", "getPluginStateAsync", "setPluginStateAsync", "statePool_"} {
		if !strings.Contains(h, needle) {
			t.Fatalf("EngineAudioDevice.h missing %q (E-M5 async state IO)", needle)
		}
	}
	impl, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "EngineAudioDevice.cpp"))
	if err != nil {
		t.Skip(err.Error())
	}
	c := string(impl)
	for _, needle := range []string{"EngineAudioDevice::getPluginStateAsync", "EngineAudioDevice::setPluginStateAsync", "statePool_.addJob"} {
		if !strings.Contains(c, needle) {
			t.Fatalf("EngineAudioDevice.cpp missing %q (E-M5 async state IO)", needle)
		}
	}
	mainCpp, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Main.cpp"))
	if err == nil {
		m := string(mainCpp)
		if !strings.Contains(m, "getPluginStateAsync") || !strings.Contains(m, "setPluginStateAsync") {
			t.Fatal("Main.cpp must dispatch plugin state via async callbacks")
		}
	}
}

// SF2 上传上限回归：handler 4GB + router 层 4GB+32MB。
// S-M6 改为流式落盘后上限不再受内存约束；双层防线仍需保留——
// router 层防大包直拒，handler 层防 Content-Length 伪装。
func TestSoundFontUploadLimit(t *testing.T) {
	handlerData, err := os.ReadFile(filepath.Join("..", "..", "internal", "server", "handler_soundfont.go"))
	if err != nil {
		t.Skip(err.Error())
	}
	if !bytes.Contains(handlerData, []byte("maxSF2Upload = int64(4 << 30)")) {
		t.Fatal("handler_soundfont.go should enforce 4GB SF2 upload limit (streaming)")
	}
	routerData, err := os.ReadFile(filepath.Join("..", "..", "internal", "server", "router.go"))
	if err != nil {
		t.Skip(err.Error())
	}
	if !bytes.Contains(routerData, []byte("4<<30 + 32<<20")) {
		t.Fatal("router.go should allow 4GB+32MB for soundfont uploads")
	}
}
