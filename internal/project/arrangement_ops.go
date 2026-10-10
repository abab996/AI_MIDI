package project

// arrangement v2 定向修改引擎（ops API）。
//
// 编排数据（arrangement.json）有两类写入方：
//  1. 前端整份 PUT /api/projects/{id}/arrangement（本地编辑后落盘，见 arrange.ts doSave）
//  2. 定向修改 POST /api/projects/{id}/arrangement/ops（AI 工具与前端机架操作共用）
//
// 定向修改只描述"改什么"，服务端在项目级写锁内读-改-写整份 JSON：读旧文件、
// 只动被点名的字段、其余原样保留（clips/notes 等前端私有结构一个字节都不碰）。
// 这样 AI 不必构造完整工程快照，也不会把前端的未保存编辑整份覆盖掉。
//
// v2 相对 v1 的增量（详见《架构升级实施计划》3.2.2）：
//   - tracks[].source 由 {type,wave,tone,libId,presetId} 改为 {itemId,presetId}
//   - 新增 rack.items（项目乐器箱，音源的唯一候选集）
//   - 新增 pianoRoll（钢琴窗音源选择，跨会话保持）
//   - 新增 mixer（P4 混音台；本阶段只登记 setTrackMix 的 pan）
//   - 新增 rev（单调递增修订号，SSE 事件与响应据此判断新鲜度）

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"melodymate/internal/soundlib"
)

// ArrangementVersion 编排数据当前结构版本
const ArrangementVersion = 2

// 机架条目种类（与前端 frontend/src/arrangement/rack.ts 的 KIND_* 一一对应）
const (
	RackKindSynth  = "synth"  // 内置多波形合成器
	RackKindSF2    = "sf2"    // SF2 音色库（含内置 GeneralUser GS）
	RackKindPack   = "pack"   // 采样包（P3 素材库消费）
	RackKindVST3   = "vst3"   // VST3 插件（P5）
	RackKindMM     = "mm"     // MM 插件（P6）
	RackKindSample = "sample" // 单个采样文件（拖到轨道 = 音频 Clip）
)

// 内置虚拟音源：不属于注册表，由程序自带能力兜底。
// builtin:synth 走引擎内置波形声部；builtin:gm 走随包 GeneralUser GS。
const (
	BuiltinSynthSourceID = "builtin:synth"
	BuiltinGMSourceID    = "builtin:gm"
)

// 变更类别（SSE 事件与前端热应用据此决定重绘哪一块）
const (
	ChangeRack      = "rack"
	ChangeTracks    = "tracks"
	ChangeMixer     = "mixer"
	ChangePianoRoll = "pianoRoll"
	ChangeTransport = "transport"
)

// RackItem 项目乐器箱条目（arrangement.rack.items[i]）
type RackItem struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SourceID string `json:"sourceId,omitempty"`
	PresetID string `json:"presetId,omitempty"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"` // sample 专用：采样文件绝对路径
	Pinned   bool   `json:"pinned,omitempty"`
}

// RackMaxItems 机架条目上限。机架是项目级选集而非全库镜像，
// 无上限时 AI 一次批量入架能把机架刷成几百条，选择器就废了。
const RackMaxItems = 64

// OpsResult 定向修改结果
type OpsResult struct {
	Arrangement map[string]any `json:"arrangement"`
	Rev         int64          `json:"rev"`
	Summary     []string       `json:"summary"`
	Changed     []string       `json:"changed"`
}

/* ═══════════ 项目级写锁 ═══════════ */

var arrangementLocks sync.Map // projectID -> *sync.Mutex

// LockArrangement 取项目级编排写锁，返回释放函数。
// 定向修改与整份 PUT 共用它：两条写入路径若不互斥，
// "AI 改音源"与"前端保存"并发时会互相整份覆盖。
func LockArrangement(projectID string) func() {
	v, _ := arrangementLocks.LoadOrStore(projectID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

/* ═══════════ 主入口 ═══════════ */

// LoadArrangementV2 读取编排数据并补全为 v2（不落盘）。
// 只读路径（AI 的 list_rack / 列轨道）用它：拿到的结构与 ApplyOps 一致，
// 不必先写一次文件才能读懂 v1 老工程。
func LoadArrangementV2(projectID string) map[string]any {
	raw, err := ReadArrangement(projectID)
	if err != nil {
		return nil
	}
	arr := decodeArrangement(raw)
	if arr == nil {
		return nil
	}
	MigrateArrangementV2(arr)
	return arr
}

// ApplyOps 对项目编排数据做一组定向修改并原子落盘。
// 返回修改后的完整编排数据（前端拉取/热应用直接用它，不必再发一次 GET）。
func ApplyOps(projectID string, ops []map[string]any) (*OpsResult, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("缺少项目 id")
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("ops 不能为空")
	}
	if len(ops) > 64 {
		return nil, fmt.Errorf("单次最多 64 个操作（当前 %d）", len(ops))
	}
	// 项目不存在时直接报清楚，不要让 os.WriteFile 抛一句"写入临时文件失败"
	// （AI 拿到的错误要能自己判断下一步该做什么）
	if _, err := LoadProject(projectID); err != nil {
		return nil, fmt.Errorf("项目不存在或无法读取：%s", projectID)
	}

	unlock := LockArrangement(projectID)
	defer unlock()

	raw, err := ReadArrangement(projectID)
	if err != nil {
		return nil, err
	}
	arr := decodeArrangement(raw)
	if arr == nil {
		arr = NewArrangement()
	}
	MigrateArrangementV2(arr)

	ctx := &opsCtx{arr: arr}
	for i, op := range ops {
		name := normalizeOpName(stringField(op, "op"))
		if name == "" {
			return nil, fmt.Errorf("第 %d 个操作缺少 op 字段", i+1)
		}
		if err := ctx.apply(name, op); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}

	arr["version"] = ArrangementVersion
	rev := intField(arr, "rev") + 1
	arr["rev"] = rev

	data, err := json.Marshal(arr)
	if err != nil {
		return nil, fmt.Errorf("序列化编排数据失败: %w", err)
	}
	if err := WriteArrangement(projectID, data); err != nil {
		return nil, err
	}
	return &OpsResult{
		Arrangement: arr,
		Rev:         rev,
		Summary:     ctx.summary,
		Changed:     ctx.changedList(),
	}, nil
}

// opsCtx 单次 ops 请求的累积状态
type opsCtx struct {
	arr     map[string]any
	summary []string
	changed map[string]bool
}

func (c *opsCtx) mark(kinds ...string) {
	if c.changed == nil {
		c.changed = map[string]bool{}
	}
	for _, k := range kinds {
		c.changed[k] = true
	}
}

func (c *opsCtx) note(format string, args ...any) {
	c.summary = append(c.summary, fmt.Sprintf(format, args...))
}

func (c *opsCtx) changedList() []string {
	out := make([]string, 0, len(c.changed))
	for k := range c.changed {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *opsCtx) apply(name string, op map[string]any) error {
	switch name {
	case "addtrack":
		return c.addTrack(op)
	case "removetrack":
		return c.removeTrack(op)
	case "renametrack":
		return c.renameTrack(op)
	case "settracksource", "settrackinstrument":
		return c.setTrackSource(op)
	case "settrackmix":
		return c.setTrackMix(op)
	case "addrackitem":
		return c.addRackItem(op)
	case "setrackitem":
		return c.setRackItem(op)
	case "removerackitem":
		return c.removeRackItem(op)
	case "setfxslot":
		return c.setFxSlot(op)
	case "setmaster":
		return c.setMaster(op)
	case "setpianoroll":
		return c.setPianoRoll(op)
	case "setbpm":
		return c.setBPM(op)
	case "setloop":
		return c.setLoop(op)
	default:
		return fmt.Errorf("未知操作 %q", name)
	}
}

/* ═══════════ 轨道操作 ═══════════ */

func (c *opsCtx) addTrack(op map[string]any) error {
	tracks := trackList(c.arr)
	if len(tracks) >= MaxArrangementTracks {
		return fmt.Errorf("轨道数已达上限 %d", MaxArrangementTracks)
	}
	name := strings.TrimSpace(stringField(op, "name"))
	if name == "" {
		name = fmt.Sprintf("Track %d", len(tracks)+1)
	}
	track := map[string]any{
		"id":     newTrackID(),
		"name":   name,
		"color":  pickTrackColor(len(tracks)),
		"mute":   false,
		"solo":   false,
		"volume": 0.8,
		"clips":  []any{},
	}
	itemID := strings.TrimSpace(stringField(op, "itemId"))
	if itemID == "" {
		itemID = c.defaultItemID()
	}
	if itemID != "" {
		item, adopted, ierr := c.itemOrAdopt(itemID, strings.TrimSpace(stringField(op, "presetId")))
		if ierr != nil {
			return ierr
		}
		if adopted {
			c.mark(ChangeRack)
			c.note("音源 %s 已自动加入机架", stringField(item, "name"))
		}
		track["source"] = map[string]any{
			"itemId":   stringField(item, "id"),
			"presetId": strings.TrimSpace(stringField(op, "presetId")),
		}
	}

	index := len(tracks)
	if _, ok := op["index"]; ok {
		idx, err := intFieldStrict(op, "index")
		if err != nil {
			return err
		}
		if idx < 0 || idx > len(tracks) {
			return fmt.Errorf("index %d 超出范围（0..%d）", idx, len(tracks))
		}
		index = idx
	}
	tracks = append(tracks, nil)
	copy(tracks[index+1:], tracks[index:])
	tracks[index] = track
	c.arr["tracks"] = tracks
	c.mark(ChangeTracks)
	c.note("新增轨道「%s」(#%d)", name, index+1)
	return nil
}

func (c *opsCtx) removeTrack(op map[string]any) error {
	tracks := trackList(c.arr)
	idx, err := c.trackIndex(op)
	if err != nil {
		return err
	}
	track, err := trackMap(tracks, idx)
	if err != nil {
		return err
	}
	name := stringField(track, "name")
	tracks = append(tracks[:idx], tracks[idx+1:]...)
	if len(tracks) == 0 {
		// 与前端 deserialize 对齐：至少保留一条轨道
		tracks = append(tracks, c.fallbackTrack())
	}
	c.arr["tracks"] = tracks
	c.mark(ChangeTracks, ChangeMixer)
	c.note("删除轨道「%s」", name)
	return nil
}

func (c *opsCtx) renameTrack(op map[string]any) error {
	tracks := trackList(c.arr)
	idx, err := c.trackIndex(op)
	if err != nil {
		return err
	}
	track, err := trackMap(tracks, idx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(stringField(op, "name"))
	if name == "" {
		return fmt.Errorf("name 不能为空")
	}
	if len([]rune(name)) > 60 {
		return fmt.Errorf("轨道名过长（上限 60 字符）")
	}
	track["name"] = name
	c.mark(ChangeTracks)
	c.note("轨道 #%d 更名为「%s」", idx+1, name)
	return nil
}

func (c *opsCtx) setTrackSource(op map[string]any) error {
	tracks := trackList(c.arr)
	idx, err := c.trackIndex(op)
	if err != nil {
		return err
	}
	track, err := trackMap(tracks, idx)
	if err != nil {
		return err
	}
	itemID := strings.TrimSpace(stringField(op, "itemId"))
	if itemID == "" {
		itemID = strings.TrimSpace(stringField(op, "rackItemId"))
	}
	if itemID == "" {
		return fmt.Errorf("缺少 itemId（音源必须先加入机架）")
	}
	presetID := strings.TrimSpace(stringField(op, "presetId"))
	item, adopted, err := c.itemOrAdopt(itemID, presetID)
	if err != nil {
		return err
	}
	if adopted {
		c.mark(ChangeRack)
		c.note("音源 %s 已自动加入机架", stringField(item, "name"))
	}
	if presetID == "" {
		presetID = stringField(item, "presetId")
	}
	track["source"] = map[string]any{"itemId": stringField(item, "id"), "presetId": presetID}
	c.mark(ChangeTracks)
	c.note("轨道 #%d「%s」音源 → %s%s", idx+1, stringField(track, "name"),
		stringField(item, "name"), presetSuffix(presetID))
	return nil
}

func (c *opsCtx) setTrackMix(op map[string]any) error {
	tracks := trackList(c.arr)
	idx, err := c.trackIndex(op)
	if err != nil {
		return err
	}
	track, err := trackMap(tracks, idx)
	if err != nil {
		return err
	}
	trackID := stringField(track, "id")
	if trackID == "" {
		return fmt.Errorf("轨道缺少 id，无法定位混音通道")
	}
	parts := []string{}

	if v, ok := op["gain"]; ok && v != nil {
		gain, gerr := floatFieldStrict(v, "gain")
		if gerr != nil {
			return gerr
		}
		if gain < 0 || gain > 1 {
			return fmt.Errorf("gain 超出范围（0.0 ~ 1.0）")
		}
		track["volume"] = gain
		mixChannel(c.arr, trackID)["gain"] = gain
		parts = append(parts, fmt.Sprintf("gain=%.2f", gain))
	}
	if v, ok := op["pan"]; ok && v != nil {
		pan, perr := floatFieldStrict(v, "pan")
		if perr != nil {
			return perr
		}
		if pan < -1 || pan > 1 {
			return fmt.Errorf("pan 超出范围（-1.0 ~ 1.0）")
		}
		mixChannel(c.arr, trackID)["pan"] = pan
		parts = append(parts, fmt.Sprintf("pan=%.2f", pan))
	}
	for _, key := range []string{"mute", "solo"} {
		if v, ok := op[key]; ok && v != nil {
			b, berr := boolFieldStrict(v, key)
			if berr != nil {
				return berr
			}
			track[key] = b
			mixChannel(c.arr, trackID)[key] = b
			parts = append(parts, fmt.Sprintf("%s=%v", key, b))
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("至少要给出 gain / pan / mute / solo 之一")
	}
	c.mark(ChangeTracks, ChangeMixer)
	c.note("轨道 #%d「%s」混音：%s", idx+1, stringField(track, "name"), strings.Join(parts, " "))
	return nil
}

/* ═══════════ 机架操作 ═══════════ */

func (c *opsCtx) addRackItem(op map[string]any) error {
	sourceID := strings.TrimSpace(stringField(op, "sourceId"))
	if sourceID == "" {
		sourceID = strings.TrimSpace(stringField(op, "id"))
	}
	if sourceID == "" {
		return fmt.Errorf("缺少 sourceId（可用 list_sound_sources 查询）")
	}
	presetID := strings.TrimSpace(stringField(op, "presetId"))
	item, created, err := c.ensureItem(sourceID, presetID, strings.TrimSpace(stringField(op, "name")))
	if err != nil {
		return err
	}
	c.mark(ChangeRack)
	if created {
		c.note("机架新增 %s（%s）", stringField(item, "name"), stringField(item, "id"))
	} else {
		c.note("%s 已在机架中（%s）", stringField(item, "name"), stringField(item, "id"))
	}
	return nil
}

func (c *opsCtx) setRackItem(op map[string]any) error {
	itemID := strings.TrimSpace(stringField(op, "itemId"))
	if itemID == "" {
		return fmt.Errorf("缺少 itemId")
	}
	item, err := c.item(itemID)
	if err != nil {
		return err
	}
	parts := []string{}
	if v, ok := op["name"]; ok && v != nil {
		name := strings.TrimSpace(fmt.Sprint(v))
		if name == "" {
			return fmt.Errorf("name 不能为空")
		}
		item["name"] = name
		parts = append(parts, "名称→"+name)
	}
	if v, ok := op["presetId"]; ok && v != nil {
		item["presetId"] = strings.TrimSpace(fmt.Sprint(v))
		parts = append(parts, "默认预设→"+stringField(item, "presetId"))
	}
	if v, ok := op["pinned"]; ok && v != nil {
		b, berr := boolFieldStrict(v, "pinned")
		if berr != nil {
			return berr
		}
		item["pinned"] = b
		parts = append(parts, fmt.Sprintf("固定=%v", b))
	}
	if len(parts) == 0 {
		return fmt.Errorf("至少要给出 name / presetId / pinned 之一")
	}
	c.mark(ChangeRack)
	c.note("机架条目 %s 更新：%s", itemID, strings.Join(parts, " "))
	return nil
}

func (c *opsCtx) removeRackItem(op map[string]any) error {
	itemID := strings.TrimSpace(stringField(op, "itemId"))
	if itemID == "" {
		return fmt.Errorf("缺少 itemId")
	}
	item, err := c.item(itemID)
	if err != nil {
		return err
	}
	users := c.tracksUsing(itemID)
	force := false
	if v, ok := op["force"]; ok && v != nil {
		b, berr := boolFieldStrict(v, "force")
		if berr != nil {
			return berr
		}
		force = b
	}
	if len(users) > 0 && !force {
		return fmt.Errorf("条目「%s」仍被 %d 条轨道使用（%s）；改派音源后再删，或传 force=true 让这些轨道回落到首个机架条目",
			stringField(item, "name"), len(users), strings.Join(c.trackNames(users), "、"))
	}
	tracks := trackList(c.arr)
	fallback := c.defaultItemIDExcept(itemID)
	for _, idx := range users {
		tm, terr := trackMap(tracks, idx)
		if terr != nil {
			continue
		}
		tm["source"] = map[string]any{"itemId": fallback, "presetId": ""}
	}
	items := rackItems(c.arr)
	out := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if ok && stringField(m, "id") == itemID {
			continue
		}
		out = append(out, it)
	}
	rackMap(c.arr)["items"] = out
	c.mark(ChangeRack, ChangeTracks)
	c.note("机架移除 %s", stringField(item, "name"))
	return nil
}

/* ═══════════ 混音台（P4）：FX 槽与主总线 ═══════════ */

// MaxFxSlots 每轨 / 主总线的插入槽数量（与引擎 kMaxFxSlots、
// 前端 .mxr-fx-slot 数量一致）
const MaxFxSlots = 8

// setFxSlot 设置一个 FX 槽（轨或主总线）。
//
//	{op:"setFxSlot", track|master:true, slot:0, plugin?, bypass?, wet?, off?}
//
// 插件装载在 P4 只落状态、不装载实例（P5 的 VST3 / P6 的 MM 装载后同一份
// 状态即生效）；装插件时引擎会拒绝，前端据此提示——这里允许写入引用是因为
// 工程可能来自更高版本（前向兼容），但会把状态如实存进 arrangement.mixer。
func (c *opsCtx) setFxSlot(op map[string]any) error {
	slot, err := intFieldStrict(op, "slot")
	if err != nil {
		return err
	}
	if slot < 0 || slot >= MaxFxSlots {
		return fmt.Errorf("槽位序号超出范围（0 ~ %d）", MaxFxSlots-1)
	}
	isMaster := boolField(op, "master")
	target := ""
	ch := map[string]any(nil)
	if isMaster {
		target = "主总线"
		ch = masterMix(c.arr)
	} else {
		tracks := trackList(c.arr)
		idx, terr := c.trackIndex(op)
		if terr != nil {
			return terr
		}
		tm, terr := trackMap(tracks, idx)
		if terr != nil {
			return terr
		}
		target = fmt.Sprintf("轨道 #%d「%s」", idx+1, stringField(tm, "name"))
		ch = mixChannel(c.arr, stringField(tm, "id"))
	}

	slots, _ := ch["fx"].([]any)
	for len(slots) < MaxFxSlots {
		slots = append(slots, map[string]any{"slot": len(slots), "bypass": true, "wet": 1.0})
	}
	entry, _ := slots[slot].(map[string]any)
	if entry == nil {
		entry = map[string]any{"slot": slot, "bypass": true, "wet": 1.0}
	}

	parts := []string{}
	if v, ok := op["sourceId"]; ok {
		ref := strings.TrimSpace(fmt.Sprint(v))
		if len(ref) > 300 {
			return fmt.Errorf("音源引用过长")
		}
		entry["sourceId"] = ref
		parts = append(parts, "音源 "+ref)
	}

	if v, ok := op["plugin"]; ok && v != nil {
		ref := strings.TrimSpace(fmt.Sprint(v))
		if ref == "" {
			entry["plugin"] = ""
			parts = append(parts, "卸载插件")
		} else {
			if len(ref) > 300 {
				return fmt.Errorf("插件引用过长（上限 300 字符）")
			}
			entry["plugin"] = ref
			parts = append(parts, "插件 "+ref)
		}
	}
	if v, ok := op["bypass"]; ok && v != nil {
		b, berr := boolFieldStrict(v, "bypass")
		if berr != nil {
			return berr
		}
		entry["bypass"] = b
		parts = append(parts, fmt.Sprintf("旁通=%v", b))
	}
	if v, ok := op["wet"]; ok && v != nil {
		wet, werr := floatFieldStrict(v, "wet")
		if werr != nil {
			return werr
		}
		if wet < 0 || wet > 1 {
			return fmt.Errorf("wet 超出范围（0.0 ~ 1.0）")
		}
		entry["wet"] = wet
		parts = append(parts, fmt.Sprintf("wet=%.2f", wet))
	}
	if v, ok := op["off"]; ok && v != nil {
		b, berr := boolFieldStrict(v, "off")
		if berr != nil {
			return berr
		}
		if b {
			entry["plugin"] = ""
			entry["sourceId"] = ""
			parts = append(parts, "清空槽位")
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("至少要给出 plugin / bypass / wet / off 之一")
	}
	slots[slot] = entry
	ch["fx"] = slots
	c.mark(ChangeMixer)
	c.note("%s FX 槽 %d：%s", target, slot, strings.Join(parts, " "))
	return nil
}

// setMaster 主总线增益与（可选）主链 FX 槽。
//
//	{op:"setMaster", gain:0.9}
func (c *opsCtx) setMaster(op map[string]any) error {
	gain, err := floatFieldStrict(op["gain"], "gain")
	if err != nil {
		return err
	}
	if gain < 0 || gain > 4 {
		return fmt.Errorf("主总线增益超出范围（0.0 ~ 4.0）")
	}
	masterMix(c.arr)["gain"] = gain
	c.mark(ChangeMixer)
	c.note("主总线增益 → %.2f", gain)
	return nil
}

// masterMix 取（必要时创建）主总线混音状态
func masterMix(arr map[string]any) map[string]any {
	mixer, _ := arr["mixer"].(map[string]any)
	if mixer == nil {
		mixer = map[string]any{}
		arr["mixer"] = mixer
	}
	master, _ := mixer["master"].(map[string]any)
	if master == nil {
		master = map[string]any{"gain": 1.0}
		mixer["master"] = master
	}
	if _, ok := master["fx"]; !ok {
		master["fx"] = []any{}
	}
	return master
}

func (c *opsCtx) setPianoRoll(op map[string]any) error {
	itemID := strings.TrimSpace(stringField(op, "itemId"))
	if itemID == "" {
		return fmt.Errorf("缺少 itemId")
	}
	item, adopted, err := c.itemOrAdopt(itemID, strings.TrimSpace(stringField(op, "presetId")))
	if err != nil {
		return err
	}
	if adopted {
		c.mark(ChangeRack)
		c.note("音源 %s 已自动加入机架", stringField(item, "name"))
	}
	presetID := strings.TrimSpace(stringField(op, "presetId"))
	if presetID == "" {
		presetID = stringField(item, "presetId")
	}
	c.arr["pianoRoll"] = map[string]any{"itemId": stringField(item, "id"), "presetId": presetID}
	c.mark(ChangePianoRoll)
	c.note("钢琴窗音源 → %s%s", stringField(item, "name"), presetSuffix(presetID))
	return nil
}

/* ═══════════ 走带 ═══════════ */

func (c *opsCtx) setBPM(op map[string]any) error {
	bpm, err := intFieldStrict(op, "bpm")
	if err != nil {
		return err
	}
	if bpm < 30 || bpm > 300 {
		return fmt.Errorf("bpm 超出范围（30 ~ 300）")
	}
	c.arr["bpm"] = bpm
	c.mark(ChangeTransport)
	c.note("速度 → %d BPM", bpm)
	return nil
}

func (c *opsCtx) setLoop(op map[string]any) error {
	loop, _ := c.arr["loop"].(map[string]any)
	if loop == nil {
		loop = map[string]any{"on": false, "start": 0.0, "end": 16.0}
	}
	parts := []string{}
	if v, ok := op["on"]; ok && v != nil {
		b, err := boolFieldStrict(v, "on")
		if err != nil {
			return err
		}
		loop["on"] = b
		parts = append(parts, fmt.Sprintf("on=%v", b))
	}
	for _, key := range []string{"start", "end"} {
		if v, ok := op[key]; ok && v != nil {
			f, err := floatFieldStrict(v, key)
			if err != nil {
				return err
			}
			if f < 0 || f > 100000 {
				return fmt.Errorf("%s 超出范围", key)
			}
			loop[key] = f
			parts = append(parts, fmt.Sprintf("%s=%.3f", key, f))
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("至少要给出 on / start / end 之一")
	}
	start := floatField(loop, "start")
	end := floatField(loop, "end")
	if end <= start {
		return fmt.Errorf("循环终点必须大于起点（%.3f → %.3f）", start, end)
	}
	c.arr["loop"] = loop
	c.mark(ChangeTransport)
	c.note("循环区间：%s", strings.Join(parts, " "))
	return nil
}

/* ═══════════ 机架条目解析（服务端与前端同规则） ═══════════ */

// resolveRackKind 把注册表音源种类映射为机架条目种类
func resolveRackKind(sourceID string) string {
	switch sourceID {
	case BuiltinSynthSourceID:
		return RackKindSynth
	case BuiltinGMSourceID:
		return RackKindSF2
	}
	switch src, err := soundlib.Get(sourceID); {
	case err == nil:
		switch src.Kind {
		case soundlib.KindSF2:
			return RackKindSF2
		case soundlib.KindSamplePack:
			return RackKindPack
		case soundlib.KindVST3:
			return RackKindVST3
		case soundlib.KindMM:
			return RackKindMM
		}
		return src.Kind
	default:
		return ""
	}
}

// defaultPresetFor 条目默认预设：合成器给锯齿波，SF2 给 0:0（引擎首个预设）
func defaultPresetFor(kind string) string {
	switch kind {
	case RackKindSynth:
		return "sawtooth"
	case RackKindSF2:
		return "0:0"
	}
	return ""
}

// rackItemID 由 (kind,sourceId,presetId,path) 派生稳定 id：
// 同一音源重复入架不会产生重复条目，迁移反复执行也得到同一份机架。
//
// 用 FNV-1a 64（对 UTF-8 字节）而不是 sha256：前端要在本地离线造出同一个
// id（v1 工程在浏览器里迁移后再整份 PUT），JS 侧同步实现 sha256 太重，
// FNV 只要十行且两端易对齐——对应实现见 frontend/src/arrangement/rack.ts
// 的 fnv1a64/rackItemId，两处改动必须同步（tools/dev/rack_check.mjs 用
// 固定向量盯住这一点）。
func rackItemID(kind, sourceID, presetID, path string) string {
	key := strings.Join([]string{kind, strings.ToLower(sourceID), presetID, strings.ToLower(path)}, "\x00")
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= fnvPrime64
	}
	sum := []byte{
		byte(h >> 56), byte(h >> 48), byte(h >> 40), byte(h >> 32),
		byte(h >> 24), byte(h >> 16), byte(h >> 8), byte(h),
	}
	return "rk_" + hex.EncodeToString(sum)[:12]
}

// sourceDisplayName 音源显示名（AI 与前端都读它）
func sourceDisplayName(sourceID string) string {
	switch sourceID {
	case BuiltinSynthSourceID:
		return "内置合成器"
	case BuiltinGMSourceID:
		return "内置 GeneralUser GS"
	}
	if src, err := soundlib.Get(sourceID); err == nil && strings.TrimSpace(src.Name) != "" {
		return src.Name
	}
	return sourceID
}

// itemDisplayName 条目显示名：SF2 带预设名便于区分同一音色库的不同音色
func itemDisplayName(kind, sourceID, presetID string) string {
	base := sourceDisplayName(sourceID)
	if presetID == "" || kind == RackKindSynth {
		return base
	}
	if kind == RackKindSF2 {
		if name := soundlib.SF2PresetName(sourceID, presetID); name != "" {
			return base + " › " + name
		}
	}
	return base + " › " + presetID
}

// generalUserSourceID 随包 GeneralUser GS 在注册表中的 id（找不到返回空）
func generalUserSourceID() string {
	for _, v := range soundlib.List(soundlib.KindSF2, "") {
		n := strings.ToLower(strings.ReplaceAll(v.Name, " ", ""))
		if strings.Contains(n, "generaluser") {
			return v.ID
		}
	}
	return ""
}

// newRackItem 按 sourceId 构造条目（kind 与名称由注册表/内置规则决定）。
// sourceId 也可以是采样文件的绝对路径（AI 直接把一个 wav 拖进机架的场景）。
func newRackItem(sourceID, presetID, name string) (RackItem, error) {
	sourceID = strings.TrimSpace(sourceID)
	kind := resolveRackKind(sourceID)
	samplePath := ""
	if kind == "" && soundlib.IsAudioPath(sourceID) {
		if info, err := os.Stat(sourceID); err == nil && !info.IsDir() {
			kind, samplePath = RackKindSample, filepath.Clean(sourceID)
		}
	}
	if kind == "" {
		return RackItem{}, fmt.Errorf("音源不存在：%s（可用 list_sound_sources 查询可用音源）", sourceID)
	}
	if kind == RackKindSample {
		presetID = ""
		if strings.TrimSpace(name) == "" {
			name = strings.TrimSuffix(filepath.Base(samplePath), filepath.Ext(samplePath))
		}
		return RackItem{
			ID:       rackItemID(kind, samplePath, "", samplePath),
			Kind:     kind,
			SourceID: samplePath,
			Name:     strings.TrimSpace(name),
			Path:     samplePath,
		}, nil
	}
	if presetID == "" {
		presetID = defaultPresetFor(kind)
	}
	if strings.TrimSpace(name) == "" {
		name = itemDisplayName(kind, sourceID, presetID)
	}
	return RackItem{
		ID:       rackItemID(kind, sourceID, presetID, ""),
		Kind:     kind,
		SourceID: sourceID,
		PresetID: presetID,
		Name:     strings.TrimSpace(name),
	}, nil
}

func rackItemFromMap(m map[string]any) RackItem {
	return RackItem{
		ID:       stringField(m, "id"),
		Kind:     stringField(m, "kind"),
		SourceID: stringField(m, "sourceId"),
		PresetID: stringField(m, "presetId"),
		Name:     stringField(m, "name"),
		Path:     stringField(m, "path"),
		Pinned:   boolField(m, "pinned"),
	}
}

func (it RackItem) toMap() map[string]any {
	m := map[string]any{
		"id":       it.ID,
		"kind":     it.Kind,
		"sourceId": it.SourceID,
		"name":     it.Name,
	}
	if it.PresetID != "" {
		m["presetId"] = it.PresetID
	}
	if it.Path != "" {
		m["path"] = it.Path
	}
	if it.Pinned {
		m["pinned"] = true
	}
	return m
}

/* ═══════════ opsCtx 的机架访问 ═══════════ */

// item 按 id 取机架条目（严格：不在架即报错）
func (c *opsCtx) item(itemID string) (map[string]any, error) {
	if m := rackItemByID(c.arr, itemID); m != nil {
		return m, nil
	}
	return nil, fmt.Errorf("音源不在机架中：%s（先用 add_rack_item 加入机架）", itemID)
}

// itemOrAdopt 取机架条目；不在架时按 sourceId 自动入架。
//
// AI 很容易把 sourceId（sf2_xxx / builtin:synth）当成 itemId 传，
// 直接报错会让它反复试错。这里在"必须来自机架"的前提下自动补一步入架：
// 机架仍是唯一候选集（轨道最终引用的还是一条机架条目），只是不必手写两步。
func (c *opsCtx) itemOrAdopt(ref, presetID string) (map[string]any, bool, error) {
	if m := rackItemByID(c.arr, ref); m != nil {
		return m, false, nil
	}
	if m, created, err := ensureItemIn(c.arr, ref, presetID, ""); err == nil {
		return m, created, nil
	}
	return nil, false, fmt.Errorf("音源不在机架中：%s（先用 list_sound_sources 查可用音源，再 add_rack_item 入架）", ref)
}

// ensureItem 按 sourceId 入架（已在架则复用），返回条目与是否新建
func (c *opsCtx) ensureItem(sourceID, presetID, name string) (map[string]any, bool, error) {
	return ensureItemIn(c.arr, sourceID, presetID, name)
}

// ensureItemIn 包级入架实现（opsCtx 与迁移共用）
func ensureItemIn(arr map[string]any, sourceID, presetID, name string) (map[string]any, bool, error) {
	item, err := newRackItem(sourceID, presetID, name)
	if err != nil {
		return nil, false, err
	}
	if existing := rackItemByID(arr, item.ID); existing != nil {
		return existing, false, nil
	}
	if len(rackItems(arr)) >= RackMaxItems {
		return nil, false, fmt.Errorf("机架条目已达上限 %d", RackMaxItems)
	}
	m := item.toMap()
	rackMap(arr)["items"] = append(rackItems(arr), m)
	return m, true, nil
}

// rackItemByID 按 id 取机架条目（不存在返回 nil）
func rackItemByID(arr map[string]any, itemID string) map[string]any {
	if itemID == "" {
		return nil
	}
	for _, it := range rackItems(arr) {
		if m, ok := it.(map[string]any); ok && stringField(m, "id") == itemID {
			return m
		}
	}
	return nil
}

// firstRackItemID 首个机架条目 id（无条目时兜底建一个内置合成器条目）
func firstRackItemID(arr map[string]any) string {
	for _, it := range rackItems(arr) {
		if m, ok := it.(map[string]any); ok {
			if id := stringField(m, "id"); id != "" {
				return id
			}
		}
	}
	if m, _, err := ensureItemIn(arr, BuiltinSynthSourceID, "", ""); err == nil {
		return stringField(m, "id")
	}
	return ""
}

// tracksUsing 返回使用该机架条目的轨道下标（升序）
func (c *opsCtx) tracksUsing(itemID string) []int {
	var out []int
	for i, t := range trackList(c.arr) {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if stringField(mapField(tm, "source"), "itemId") == itemID {
			out = append(out, i)
		}
	}
	return out
}

// trackNames 轨道名列表（错误提示里点名到轨道，AI 才知道改哪几条）
func (c *opsCtx) trackNames(indexes []int) []string {
	tracks := trackList(c.arr)
	out := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		if tm, err := trackMap(tracks, idx); err == nil {
			out = append(out, fmt.Sprintf("#%d %s", idx+1, stringField(tm, "name")))
		}
	}
	if len(out) == 0 {
		return []string{"（轨道信息不可用）"}
	}
	return out
}

func (c *opsCtx) trackIndex(op map[string]any) (int, error) {
	tracks := trackList(c.arr)
	ref, ok := op["track"]
	if !ok || ref == nil {
		return 0, fmt.Errorf("缺少 track（轨道序号从 0 开始，或传轨道 id/名称）")
	}
	return findTrackIndex(tracks, ref)
}

// defaultItemID 首个机架条目 id（无条目时兜底建一个内置合成器条目）
func (c *opsCtx) defaultItemID() string {
	return c.defaultItemIDExcept("")
}

func (c *opsCtx) defaultItemIDExcept(exclude string) string {
	for _, it := range rackItems(c.arr) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id := stringField(m, "id")
		if id != "" && id != exclude {
			return id
		}
	}
	if m, _, err := ensureItemIn(c.arr, BuiltinSynthSourceID, "", ""); err == nil {
		return stringField(m, "id")
	}
	return ""
}

func (c *opsCtx) fallbackTrack() map[string]any {
	track := map[string]any{
		"id":     newTrackID(),
		"name":   "Track 1",
		"color":  pickTrackColor(0),
		"mute":   false,
		"solo":   false,
		"volume": 0.8,
		"clips":  []any{},
	}
	if id := c.defaultItemID(); id != "" {
		track["source"] = map[string]any{"itemId": id, "presetId": ""}
	}
	return track
}

/* ═══════════ v1 → v2 迁移（服务端侧，与前端 deserialize 同规则） ═══════════ */

// NewArrangement 默认的 v2 编排数据结构（与前端 defaultState 对齐：
// 一条轨道 + 一个内置合成器条目，轨道指向它）。
// 新工程直接对轨道做定向修改时不该出现"没有任何轨道"的中间态。
func NewArrangement() map[string]any {
	arr := map[string]any{
		"version": ArrangementVersion,
		"rev":     int64(0),
		"bpm":     120,
		"snap":    0.25,
		"ppb":     26,
		"loop":    map[string]any{"on": false, "start": 0.0, "end": 16.0},
		"tracks":  []any{},
		"rack":    map[string]any{"items": []any{}},
		"mixer":   map[string]any{"channels": map[string]any{}},
	}
	item, _, err := ensureItemIn(arr, BuiltinSynthSourceID, "sawtooth", "合成器 · 锯齿波")
	itemID, presetID := "", ""
	if err == nil {
		itemID = stringField(item, "id")
		presetID = stringField(item, "presetId")
	}
	arr["tracks"] = []any{map[string]any{
		"id":     newTrackID(),
		"name":   "Track 1",
		"color":  pickTrackColor(0),
		"mute":   false,
		"solo":   false,
		"volume": 0.8,
		"source": map[string]any{"itemId": itemID, "presetId": presetID},
		"clips":  []any{},
	}}
	arr["pianoRoll"] = map[string]any{"itemId": itemID, "presetId": presetID}
	return arr
}

// MigrateArrangementV2 把 v1（或缺失字段）的编排数据就地补全为 v2。
// 幂等：反复执行结果一致（机架条目 id 由音源派生）。
//
// 迁移规则（与 frontend/src/arrangement/rack.ts migrateV1Source 对应）：
//
//	v1 synth{wave}        → 机架「内置合成器」条目 presetId=wave
//	v1 builtin{piano}     → 注册表里的 GeneralUser GS 0:0（找不到则 builtin:gm 0:0）
//	v1 builtin{strings}   → 同上 0:48
//	v1 sf2{libId,presetId}→ 机架 SF2 条目（sourceId 取 sourceId/libId）
func MigrateArrangementV2(arr map[string]any) {
	if arr == nil {
		return
	}
	if rackMap(arr) == nil {
		arr["rack"] = map[string]any{"items": []any{}}
	}
	if _, ok := arr["mixer"].(map[string]any); !ok {
		arr["mixer"] = map[string]any{"channels": map[string]any{}}
	}
	if _, ok := arr["pianoRoll"].(map[string]any); !ok {
		arr["pianoRoll"] = map[string]any{"itemId": "", "presetId": ""}
	}

	// 迁移前先记下旧 source，迁移中要把它们换成 itemId 引用
	tracks := trackList(arr)
	needTrackSourceFix := false
	for _, t := range tracks {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		src := mapField(tm, "source")
		if src == nil {
			needTrackSourceFix = true
			break
		}
		if stringField(src, "itemId") == "" {
			needTrackSourceFix = true
			break
		}
	}
	if !needTrackSourceFix && floatField(arr, "version") >= ArrangementVersion {
		arr["version"] = ArrangementVersion
		return // 已是 v2，不再重复扫描（保持幂等且不误改）
	}

	for _, t := range tracks {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		src := mapField(tm, "source")
		if src == nil {
			continue
		}
		if itemID := stringField(src, "itemId"); itemID != "" {
			// v2 引用：条目被外部删掉时回落到首个条目，避免轨道指向空音源
			if rackItemByID(arr, itemID) == nil {
				tm["source"] = map[string]any{"itemId": firstRackItemID(arr), "presetId": ""}
			}
			continue
		}
		item := rackItemFromV1Source(src)
		if item.ID == "" {
			continue
		}
		if rackItemByID(arr, item.ID) == nil {
			upsertRackItem(arr, item)
		}
		tm["source"] = map[string]any{"itemId": item.ID, "presetId": item.PresetID}
	}

	// 钢琴窗：v1 无持久化字段，指向首个机架条目即可（前端会按需回填）
	pr := mapField(arr, "pianoRoll")
	if stringField(pr, "itemId") == "" {
		if items := rackItems(arr); len(items) > 0 {
			if m, ok := items[0].(map[string]any); ok {
				pr["itemId"] = stringField(m, "id")
				pr["presetId"] = stringField(m, "presetId")
			}
		}
	}
	arr["version"] = ArrangementVersion
	if _, ok := arr["rev"]; !ok {
		arr["rev"] = int64(0)
	}
}

// rackItemFromV1Source v1 轨道音源 → 机架条目
func rackItemFromV1Source(src map[string]any) RackItem {
	switch stringField(src, "type") {
	case "synth":
		wave := stringField(src, "wave")
		if wave == "" {
			wave = "sawtooth"
		}
		it, err := newRackItem(BuiltinSynthSourceID, wave, "")
		if err != nil {
			return RackItem{}
		}
		return it
	case "builtin":
		program := "0:0"
		label := "钢琴"
		if stringField(src, "tone") == "strings" {
			program, label = "0:48", "弦乐群"
		}
		if id := generalUserSourceID(); id != "" {
			return RackItem{
				ID:       rackItemID(RackKindSF2, id, program, ""),
				Kind:     RackKindSF2,
				SourceID: id,
				PresetID: program,
				Name:     sourceDisplayName(id) + " › " + label,
			}
		}
		return RackItem{
			ID:       rackItemID(RackKindSF2, BuiltinGMSourceID, program, ""),
			Kind:     RackKindSF2,
			SourceID: BuiltinGMSourceID,
			PresetID: program,
			Name:     "内置 · " + label,
		}
	case "sf2":
		sourceID := stringField(src, "sourceId")
		if sourceID == "" {
			sourceID = stringField(src, "libId")
		}
		presetID := stringField(src, "presetId")
		if sourceID == "" {
			/* 老工程里常见"只有 path/bank/program"的音源（历史 IndexedDB 记录已丢）。
			   **不能**因此不给 itemId：那样这条轨就没有机架归属，违反"音源只能来自
			   机架"，界面也无法显示为"音源缺失"。这里按老路径造一个**缺失条目**
			   （sourceId 用 legacy: 前缀，注册表里必然查不到 → 前端 resolve 得到
			   missing，显示「♪ ⚠ 音源缺失」并零增益出声），用户重新选源即可修好。 */
			path := stringField(src, "path")
			if path == "" {
				return RackItem{}
			}
			base := path
			if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
				base = base[i+1:]
			}
			if presetID == "" {
				bank := intField(src, "bank")
				program := intField(src, "program")
				presetID = fmt.Sprintf("%d:%d", bank, program)
			}
			sourceID = "legacy:" + strings.ToLower(base)
			return RackItem{
				ID:       rackItemID(RackKindSF2, sourceID, presetID, path),
				Kind:     RackKindSF2,
				SourceID: sourceID,
				PresetID: presetID,
				Name:     "♪ ⚠ 音源缺失 · " + base,
			}
		}
		if presetID == "" {
			presetID = "0:0"
		}
		// 历史工程的 libId 是 IndexedDB 记录 id，注册表里查不到时按名称提示
		kind := resolveRackKind(sourceID)
		if kind == "" {
			kind = RackKindSF2
		}
		name := stringField(src, "label")
		if name == "" || kind == RackKindSF2 {
			name = itemDisplayName(kind, sourceID, presetID)
		}
		return RackItem{
			ID:       rackItemID(kind, sourceID, presetID, ""),
			Kind:     kind,
			SourceID: sourceID,
			PresetID: presetID,
			Name:     name,
		}
	}
	return RackItem{}
}

// upsertRackItem 写入或替换机架条目（同 id 覆盖）
func upsertRackItem(arr map[string]any, item RackItem) {
	if item.ID == "" {
		return
	}
	items := rackItems(arr)
	for i, it := range items {
		if m, ok := it.(map[string]any); ok && stringField(m, "id") == item.ID {
			items[i] = item.toMap()
			rackMap(arr)["items"] = items
			return
		}
	}
	rackMap(arr)["items"] = append(items, item.toMap())
}

/* ═══════════ 通用 map 工具 ═══════════ */

// MaxArrangementTracks 编排轨上限（与前端/引擎轨位约定一致：0..28 为编曲轨）
const MaxArrangementTracks = 29

func decodeArrangement(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var arr map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || arr == nil {
		return nil
	}
	return arr
}

func rackMap(arr map[string]any) map[string]any {
	m, _ := arr["rack"].(map[string]any)
	return m
}

func rackItems(arr map[string]any) []any {
	rm := rackMap(arr)
	if rm == nil {
		return []any{}
	}
	items, _ := rm["items"].([]any)
	if items == nil {
		return []any{}
	}
	return items
}

func trackList(arr map[string]any) []any {
	tracks, _ := arr["tracks"].([]any)
	if tracks == nil {
		return []any{}
	}
	return tracks
}

// trackMap 取轨道对象（下标越界或结构损坏时报错而不是 panic）
func trackMap(tracks []any, idx int) (map[string]any, error) {
	if idx < 0 || idx >= len(tracks) {
		return nil, fmt.Errorf("轨道序号 %d 超出范围", idx)
	}
	m, ok := tracks[idx].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("轨道 #%d 数据结构异常", idx+1)
	}
	return m, nil
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := m[key].(map[string]any)
	return out
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func floatField(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func intField(m map[string]any, key string) int64 {
	return int64(floatField(m, key))
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

func floatFieldStrict(v any, key string) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s 不是合法数字", key)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("%s 必须是数字", key)
	}
}

func intFieldStrict(op map[string]any, key string) (int, error) {
	f, err := floatFieldStrict(op[key], key)
	if err != nil {
		return 0, err
	}
	if f != float64(int(f)) {
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	return int(f), nil
}

func boolFieldStrict(v any, key string) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s 必须是布尔值", key)
	}
	return b, nil
}

// normalizeOpName 操作名归一：去掉分隔符与大小写差异
// （addTrack / add_track / ADD-TRACK 视为同一个操作）
func normalizeOpName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func presetSuffix(presetID string) string {
	if presetID == "" {
		return ""
	}
	return "（预设 " + presetID + "）"
}

func findTrackIndex(tracks []any, ref any) (int, error) {
	switch v := ref.(type) {
	case float64:
		idx := int(v)
		if idx < 0 || idx >= len(tracks) {
			return 0, fmt.Errorf("轨道序号 %d 超出范围（0..%d）", idx, len(tracks)-1)
		}
		return idx, nil
	case int:
		if v < 0 || v >= len(tracks) {
			return 0, fmt.Errorf("轨道序号 %d 超出范围（0..%d）", v, len(tracks)-1)
		}
		return v, nil
	case string:
		want := strings.TrimSpace(v)
		if want == "" {
			break
		}
		for i, t := range tracks {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if stringField(tm, "id") == want {
				return i, nil
			}
		}
		for i, t := range tracks {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if strings.EqualFold(stringField(tm, "name"), want) {
				return i, nil
			}
		}
		return 0, fmt.Errorf("找不到轨道 %q（可用 list 查看 id/名称）", want)
	}
	return 0, fmt.Errorf("track 必须是序号或轨道 id/名称")
}

// mixChannel 取（必要时创建）轨道混音通道，P4 混音台从这里读
func mixChannel(arr map[string]any, trackID string) map[string]any {
	mixer, _ := arr["mixer"].(map[string]any)
	if mixer == nil {
		mixer = map[string]any{}
		arr["mixer"] = mixer
	}
	channels, _ := mixer["channels"].(map[string]any)
	if channels == nil {
		channels = map[string]any{}
		mixer["channels"] = channels
	}
	ch, _ := channels[trackID].(map[string]any)
	if ch == nil {
		ch = map[string]any{"gain": 0.8, "pan": 0.0, "mute": false, "solo": false}
		channels[trackID] = ch
	}
	return ch
}

/* ═══════════ 轨道 id / 配色（与前端 makeTrack 一致的派生规则） ═══════════ */

var trackColors = []string{
	"#00B8CC", "#FF8C00", "#06D6A0", "#5B8DEF", "#E86FB4",
	"#FFD166", "#9B8CFF", "#F4743B", "#4CC9F0", "#7ED957",
}

func pickTrackColor(index int) string {
	if index < 0 {
		index = 0
	}
	return trackColors[index%len(trackColors)]
}

var trackIDCounter struct {
	sync.Mutex
	n int64
}

func newTrackID() string {
	trackIDCounter.Lock()
	defer trackIDCounter.Unlock()
	trackIDCounter.n++
	return fmt.Sprintf("tr_%d_%d", time.Now().UnixMilli(), trackIDCounter.n)
}
