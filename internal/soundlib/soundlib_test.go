package soundlib

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"melodymate/internal/config"
)

// withTempRoot 把数据根/音色库目录/settings.json 一并换绑到临时目录。
// 三者都是包级变量，不会互相重算，必须一起替换——否则测试会读写
// 真实的 Library/registry.json 与 settings.json。
func withTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib, oldSettings := config.ProjectRoot, config.LibraryDir, config.SettingsFile
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.SettingsFile = filepath.Join(root, "settings.json")
	t.Cleanup(func() {
		config.ProjectRoot, config.LibraryDir, config.SettingsFile = oldRoot, oldLib, oldSettings
	})
	return root
}

// ===== SF2 测试样本构造 =====

func riffChunk(id string, data []byte) []byte {
	out := make([]byte, 8, 8+len(data)+1)
	copy(out[0:4], id)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(data)))
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0) // RIFF 奇数长度补齐
	}
	return out
}

func riffList(typ string, subs ...[]byte) []byte {
	body := append([]byte(typ), nil...)
	for _, s := range subs {
		body = append(body, s...)
	}
	return riffChunk("LIST", body)
}

func phdrRecord(name string, bank, program int) []byte {
	rec := make([]byte, sf2PhdrRecordSize)
	copy(rec[0:20], name)
	binary.LittleEndian.PutUint16(rec[20:22], uint16(program))
	binary.LittleEndian.PutUint16(rec[22:24], uint16(bank))
	return rec
}

// buildSF2 造一个结构合法的最小 SF2：RIFF/sfbk + LIST INFO(INAM) + LIST pdta(PHDR)
func buildSF2(fontName string, presets []Preset) []byte {
	inam := append([]byte(fontName), 0)
	if len(inam)%2 == 1 {
		inam = append(inam, 0)
	}
	info := riffList("INFO", riffChunk("INAM", inam))

	pdta := make([]byte, 0, (len(presets)+1)*sf2PhdrRecordSize)
	for _, p := range presets {
		pdta = append(pdta, phdrRecord(p.Name, p.Bank, p.Program)...)
	}
	pdta = append(pdta, phdrRecord("EOP", 0, 0)...) // 终止记录
	dtal := riffList("pdta", riffChunk("PHDR", pdta))

	body := append(append([]byte("sfbk"), info...), dtal...)
	out := make([]byte, 8, 8+len(body))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(body)))
	return append(out, body...)
}

func writeSF2(t *testing.T, dir, name string, presets []Preset) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buildSF2(name, presets), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSF2RealCasing 写一份"真实写库工具"风格的最小 SF2：
// pdta 的子块用小写 id（phdr），这是 SoundFont 2.01 规范与 GeneralUser GS /
// Pianoteq 导出文件的共同写法。此前解析器只认大写 "PHDR"，导致真实音色库
// 全部解析出 0 个预设（设置页/机架/卷帘预设列表全空），而合成样本恰好用
// 大写，测试因此漏过了这条路径。
func writeSF2RealCasing(t *testing.T, dir, name string, presets []Preset) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inam := append([]byte(name), 0)
	if len(inam)%2 == 1 {
		inam = append(inam, 0)
	}
	info := riffList("INFO", riffChunk("INAM", inam))

	pdta := make([]byte, 0, (len(presets)+1)*sf2PhdrRecordSize)
	for _, p := range presets {
		pdta = append(pdta, phdrRecord(p.Name, p.Bank, p.Program)...)
	}
	pdta = append(pdta, phdrRecord("EOP", 0, 0)...)
	dtal := riffList("pdta", riffChunk("phdr", pdta)) // 小写：真实文件如此

	body := append(append([]byte("sfbk"), info...), dtal...)
	out := make([]byte, 8, 8+len(body))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(body)))
	full := append(out, body...)

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func stdPresets() []Preset {
	return []Preset{
		{Name: "Stereo Grand", Bank: 0, Program: 0},
		{Name: "Bright Yamaha Grand", Bank: 0, Program: 1},
		{Name: "Stereo Strings Fast", Bank: 0, Program: 48},
	}
}

// ===== SF2 元数据解析 =====

func TestReadSF2InfoReadsNameAndPresetCount(t *testing.T) {
	root := withTempRoot(t)
	path := writeSF2(t, filepath.Join(root, "Library", "soundfonts"), "GeneralUser GS v1.471.sf2", stdPresets())

	info, err := ReadSF2Info(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.Name != "GeneralUser GS v1.471.sf2" {
		t.Fatalf("INAM 名称不对: %q", info.Name)
	}
	if info.PresetCount != 3 {
		t.Fatalf("预设数应为 3（EOP 不计入）: %d", info.PresetCount)
	}
	// 廉价路径不读预设明细
	if len(info.Presets) != 0 {
		t.Fatalf("ReadSF2Info 不应返回预设明细: %d", len(info.Presets))
	}
}

func TestReadSF2PresetsListsBankProgram(t *testing.T) {
	root := withTempRoot(t)
	path := writeSF2(t, filepath.Join(root, "Library", "soundfonts"), "x.sf2", stdPresets())

	info, err := ReadSF2Presets(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(info.Presets) != 3 {
		t.Fatalf("应返回 3 个预设: %d", len(info.Presets))
	}
	if info.Presets[0].ID != "0:0" || info.Presets[0].Name != "Stereo Grand" {
		t.Fatalf("首个预设不对: %+v", info.Presets[0])
	}
	if info.Presets[2].ID != "0:48" || info.Presets[2].Program != 48 {
		t.Fatalf("末个预设不对: %+v", info.Presets[2])
	}
}

// 真实写库工具用小写块 id（phdr）：必须同样解析出预设
// （这是 GeneralUser GS / Pianoteq 等真实文件的实际写法）
func TestReadSF2PresetsAcceptsLowercaseChunks(t *testing.T) {
	root := withTempRoot(t)
	path := writeSF2RealCasing(t, filepath.Join(root, "Library", "soundfonts"), "real.sf2", stdPresets())

	info, err := ReadSF2Presets(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.PresetCount != 3 || len(info.Presets) != 3 {
		t.Fatalf("小写 phdr 应解析出 3 个预设: count=%d len=%d", info.PresetCount, len(info.Presets))
	}
	if info.Presets[2].ID != "0:48" || info.Presets[2].Name != "Stereo Strings Fast" {
		t.Fatalf("预设内容不对: %+v", info.Presets[2])
	}

	meta, err := ReadSF2Info(path)
	if err != nil || meta.PresetCount != 3 {
		t.Fatalf("ReadSF2Info 也应解析到 3 个预设: %+v err=%v", meta, err)
	}
}

func TestReadSF2InfoRejectsGarbage(t *testing.T) {
	root := withTempRoot(t)
	path := filepath.Join(root, "bad.sf2")
	if err := os.WriteFile(path, []byte("not a soundfont at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSF2Info(path); err == nil {
		t.Fatal("非 SF2 应报错")
	}
}

func TestReadSF2InfoTruncatedFileDoesNotPanic(t *testing.T) {
	root := withTempRoot(t)
	full := buildSF2("Cut.sf2", stdPresets())
	path := filepath.Join(root, "cut.sf2")
	// 砍掉后半段：PHDR 只剩部分记录
	if err := os.WriteFile(path, full[:len(full)-60], 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := ReadSF2Info(path)
	if err != nil {
		return // 报错也可接受，关键是不 panic
	}
	if info.PresetCount < 0 || info.PresetCount > len(stdPresets()) {
		t.Fatalf("截断文件的预设数越界: %d", info.PresetCount)
	}
}

// ===== 注册表 CRUD =====

func TestSourceCRUDAndPersistence(t *testing.T) {
	withTempRoot(t)

	added, err := Add(Source{Kind: KindSF2, Name: "我的音色", Path: filepath.Join(config.LibraryDir, "soundfonts", "mine.sf2"), Enabled: true})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if added.ID == "" || added.AddedAt == 0 {
		t.Fatalf("新增条目缺少 id/时间戳: %+v", added)
	}

	// 落盘后重新读取（模拟重启）
	got, err := Get(added.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Name != "我的音色" || !got.Enabled {
		t.Fatalf("读回内容不对: %+v", got.Source)
	}
	// 数据根内的路径应存为相对路径
	if got.Path != "Library/soundfonts/mine.sf2" {
		t.Fatalf("路径应相对化: %q", got.Path)
	}
	if filepath.Clean(ResolvePath(got.Path)) != filepath.Clean(filepath.Join(config.LibraryDir, "soundfonts", "mine.sf2")) {
		t.Fatalf("相对路径还原失败: %q", ResolvePath(got.Path))
	}

	// 更新
	updated, err := Update(added.ID, func(s *Source) error {
		s.Name = "改名了"
		s.Enabled = false
		return nil
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Name != "改名了" || updated.Enabled {
		t.Fatalf("更新未生效: %+v", updated)
	}
	if updated.UpdatedAt < updated.AddedAt {
		t.Fatal("更新应刷新 UpdatedAt")
	}

	// 删除
	removed, err := Delete(added.ID)
	if err != nil || removed.ID != added.ID {
		t.Fatalf("删除失败: %+v err=%v", removed, err)
	}
	if _, err := Get(added.ID); err != ErrNotFound {
		t.Fatalf("删除后应报 ErrNotFound, got %v", err)
	}
}

func TestAddRejectsDuplicateID(t *testing.T) {
	withTempRoot(t)
	src := Source{ID: "sf2_dup_000001", Kind: KindSF2, Name: "a", Path: "Library/soundfonts/a.sf2"}
	if _, err := Add(src); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(src); err == nil {
		t.Fatal("重复 id 应报错（改走 Update）")
	}
}

func TestListFiltersByKindAndQuery(t *testing.T) {
	withTempRoot(t)
	mustAdd(t, Source{Kind: KindSF2, Name: "Piano Library", Vendor: "Yamaha", Path: "Library/soundfonts/p.sf2"})
	mustAdd(t, Source{Kind: KindSamplePack, Name: "808 鼓组", Dirs: []string{`D:\samples\808`}, Tags: []string{"drums"}})

	if got := List(KindSF2, ""); len(got) != 1 || got[0].Name != "Piano Library" {
		t.Fatalf("按种类过滤失败: %+v", got)
	}
	if got := List("", "yamaha"); len(got) != 1 {
		t.Fatalf("按厂商搜索失败: %+v", got)
	}
	if got := List("", "808"); len(got) != 1 || got[0].Kind != KindSamplePack {
		t.Fatalf("按名称搜索失败: %+v", got)
	}
	if got := List("", "drums"); len(got) != 1 {
		t.Fatalf("按标签搜索失败: %+v", got)
	}
	if got := List("", "不存在的关键词"); len(got) != 0 {
		t.Fatalf("无关关键词应为空: %+v", got)
	}
}

func TestRuntimeReportsDiskFacts(t *testing.T) {
	root := withTempRoot(t)
	fontDir := filepath.Join(root, "Library", "soundfonts")
	writeSF2(t, fontDir, "GeneralUser GS v1.471.sf2", stdPresets())
	mustAdd(t, Source{Kind: KindSF2, Name: "GeneralUser GS v1.471", Path: filepath.Join(fontDir, "GeneralUser GS v1.471.sf2")})
	mustAdd(t, Source{Kind: KindSF2, Name: "丢了", Path: filepath.Join(fontDir, "missing.sf2")})

	byName := map[string]View{}
	for _, v := range List(KindSF2, "") {
		byName[v.Name] = v
	}
	ok := byName["GeneralUser GS v1.471"]
	if ok.Runtime.Missing || ok.Runtime.PresetCount != 3 || ok.Runtime.Size == 0 {
		t.Fatalf("在位文件实况不对: %+v", ok.Runtime)
	}
	if !byName["丢了"].Runtime.Missing {
		t.Fatal("缺失文件应标记 Missing")
	}
}

// ===== 启动迁移 =====

func TestMigrateAbsorbsSoundFontsAndMaterialDirs(t *testing.T) {
	root := withTempRoot(t)
	fontDir := filepath.Join(root, "Library", "soundfonts")
	writeSF2(t, fontDir, "GeneralUser GS v1.471.sf2", stdPresets())
	writeSF2(t, fontDir, "Pianoteq.sf2", stdPresets())
	// 上传中断残留不应入册
	if err := os.WriteFile(filepath.Join(fontDir, "broken.sf2.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sampleDir := filepath.Join(root, "samples", "808")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "kick.wav"), []byte("wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := config.LoadSettings()
	s.MaterialDirs = []string{sampleDir}
	if err := config.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate()
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if res.SoundFonts != 2 || res.Packs != 1 || res.Dirs != 1 {
		t.Fatalf("迁移计数不对: %+v", res)
	}

	fonts := List(KindSF2, "")
	if len(fonts) != 2 {
		t.Fatalf("应入册 2 个 SF2: %+v", fonts)
	}
	// 随包分发的 GeneralUser 标记为内置
	for _, f := range fonts {
		wantBuiltin := f.Name == "GeneralUser GS v1.471"
		if (f.Origin == OriginBuiltin) != wantBuiltin {
			t.Fatalf("%s 的 origin 不对: %s", f.Name, f.Origin)
		}
	}
	packs := List(KindSamplePack, "")
	if len(packs) != 1 || packs[0].Name != DefaultPackName {
		t.Fatalf("应入册 1 个采样包: %+v", packs)
	}
	if packs[0].Runtime.SampleFiles != 1 {
		t.Fatalf("采样包应统计到 1 个音频文件: %+v", packs[0].Runtime)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	root := withTempRoot(t)
	writeSF2(t, filepath.Join(root, "Library", "soundfonts"), "A.sf2", stdPresets())

	first, err := Migrate()
	if err != nil || first.SoundFonts != 1 {
		t.Fatalf("首次迁移应入册 1 个: %+v err=%v", first, err)
	}
	second, err := Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.SoundFonts != 0 {
		t.Fatalf("二次迁移应无变化: %+v", second)
	}
	if got := len(List(KindSF2, "")); got != 1 {
		t.Fatalf("重复迁移不应产生重复条目: %d", got)
	}
}

// 迁移要把 MaterialDirs 的后续新增并入采样包条目（老入口加的目录不能丢）
func TestMigrateMergesNewlyAddedMaterialDir(t *testing.T) {
	root := withTempRoot(t)
	d1 := filepath.Join(root, "samples", "one")
	d2 := filepath.Join(root, "samples", "two")
	for _, d := range []string{d1, d2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := config.LoadSettings()
	s.MaterialDirs = []string{d1}
	if err := config.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(); err != nil {
		t.Fatal(err)
	}

	// 老素材面板新增目录
	s = config.LoadSettings()
	s.MaterialDirs = []string{d1, d2}
	if err := config.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	res, err := Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if res.Dirs != 1 {
		t.Fatalf("应并入 1 个新目录: %+v", res)
	}
	packs := List(KindSamplePack, "")
	if len(packs) != 1 || len(packs[0].Dirs) != 2 {
		t.Fatalf("采样包应含 2 个目录: %+v", packs)
	}
}

// 采样包增删要反向同步到素材树读的 settings.MaterialDirs
func TestPackMutationsSyncMaterialDirs(t *testing.T) {
	root := withTempRoot(t)
	d := filepath.Join(root, "samples", "kit")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}

	added, err := Add(Source{Kind: KindSamplePack, Name: "鼓组", Dirs: []string{d}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	if got := config.LoadSettings().MaterialDirs; len(got) != 1 || !sameDirList(got, []string{d}) {
		t.Fatalf("新增采样包应写入素材目录: %+v", got)
	}

	// 停用后素材树不再列出该目录
	if _, err := Update(added.ID, func(s *Source) error { s.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	if got := config.LoadSettings().MaterialDirs; len(got) != 0 {
		t.Fatalf("停用后素材目录应为空: %+v", got)
	}

	// 删除同理
	if _, err := Update(added.ID, func(s *Source) error { s.Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	if _, err := Delete(added.ID); err != nil {
		t.Fatal(err)
	}
	SyncMaterialDirs()
	if got := config.LoadSettings().MaterialDirs; len(got) != 0 {
		t.Fatalf("删除后素材目录应为空: %+v", got)
	}
}

// 注册表损坏时不应让应用起不来：退回空注册表
func TestLoadToleratesCorruptFile(t *testing.T) {
	withTempRoot(t)
	if err := os.MkdirAll(config.LibraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FilePath(), []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := List("", ""); len(got) != 0 {
		t.Fatalf("损坏文件应退回空注册表: %+v", got)
	}
	// 损坏文件不可被迁移或新增条目静默覆盖。
	if _, err := Add(Source{Kind: KindSF2, Name: "a", Path: "Library/soundfonts/a.sf2"}); err == nil {
		t.Fatal("损坏注册表被覆盖")
	}
	if raw, _ := os.ReadFile(FilePath()); string(raw) != "{ 这不是 json" {
		t.Fatal("原始文件未保留")
	}
}

func TestSlugAndIDStability(t *testing.T) {
	withTempRoot(t)
	a := makeID("sf2", "GeneralUser GS v1.471", "Library/soundfonts/GeneralUser GS v1.471.sf2")
	b := makeID("sf2", "GeneralUser GS v1.471", "Library/soundfonts/GeneralUser GS v1.471.sf2")
	if a != b {
		t.Fatalf("同输入应得同 id: %s vs %s", a, b)
	}
	c := makeID("sf2", "GeneralUser GS v1.471", "Library/soundfonts/other.sf2")
	if a == c {
		t.Fatal("不同路径应有不同 id")
	}
	/* S-M3：引擎没有 Vorbis 解码，SF3 必须被拒绝（不能再当作 SF2 接受） */
	if !isSF2Name("a.SF2") || isSF2Name("a.sf3") || isSF2Name("a.sf2.tmp") || isSF2Name("a.wav") {
		t.Fatal("SF2 扩展名判定不对（.sf3 应被拒绝）")
	}
}

func mustAdd(t *testing.T, src Source) Source {
	t.Helper()
	out, err := Add(src)
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	return out
}
