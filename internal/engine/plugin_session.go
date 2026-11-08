package engine

import (
	"fmt"
	"melodymate/internal/plugins"
	"time"
)

type loadedPlugin struct {
	Params      map[string]any
	Ref, Format string
}

// instrumentSlot 一轨的乐器槽快照。
//
// 为什么需要：SF2 / 内置波形 / 插件乐器三者互斥，但旧实现把它们分别记在
// lastSoundFonts / lastPresets / lastVoices / loadedPlugins 四份表里。崩溃
// 重放按"音色 → 预设 → 波形 → 插件"的固定顺序执行，后写覆盖前写只能靠
// 约定；一旦某轨换了音源，旧表项仍会参与重放，恢复出错误音色（S-H3）。
// 现在每轨只保留一条记录，任何一次设置都整体替换它。
type instrumentSlot struct {
	Kind   string         `json:"kind"` // sf2 | wave | plugin
	Path   string         `json:"path,omitempty"`
	Preset [2]int         `json:"preset,omitempty"`
	HasPre bool           `json:"hasPreset,omitempty"`
	Voice  map[string]any `json:"voice,omitempty"`
	Ref    string         `json:"ref,omitempty"`
	Format string         `json:"format,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

func pluginKey(track, slot int) string { return fmt.Sprintf("%d:%d", track, slot) }
func (s *Supervisor) rememberPlugin(track, slot int, params map[string]any, ref, format string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadedPlugins == nil {
		s.loadedPlugins = map[string]loadedPlugin{}
	}
	s.loadedPlugins[pluginKey(track, slot)] = loadedPlugin{params, ref, format}
	if slot < 0 {
		s.setInstrumentSlotLocked(track, instrumentSlot{
			Kind: "plugin", Ref: ref, Format: format, Params: params,
		})
	}
}
func (s *Supervisor) SetPluginReference(track, slot int, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pluginKey(track, slot)
	p, ok := s.loadedPlugins[key]
	if ok {
		p.Ref = ref
		s.loadedPlugins[key] = p
		if slot < 0 {
			slotState := s.instrumentSlots[track]
			if slotState.Kind == "plugin" {
				slotState.Ref = ref
				s.instrumentSlots[track] = slotState
			}
		}
	}
}

// setInstrumentSlot 写入一轨乐器槽（整体替换，互斥语义由此保证）
func (s *Supervisor) setInstrumentSlot(track int, slot instrumentSlot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setInstrumentSlotLocked(track, slot)
}

func (s *Supervisor) setInstrumentSlotLocked(track int, slot instrumentSlot) {
	if s.instrumentSlots == nil {
		s.instrumentSlots = map[int]instrumentSlot{}
	}
	if slot.Kind == "" {
		delete(s.instrumentSlots, track)
		return
	}
	s.instrumentSlots[track] = slot
}

// BeginPluginSession 更新工程会话的轨道映射。
//
// 关键行为（P-H2/P-H4）：同一工程、同一代（generation）且轨道列表完全一致
// 时**不做** plugin.clear —— 插件在既有轨道上的增删/换槽由前端差分完成，
// 不再全量卸载重载，也不清空引擎里的 SF2/波形快照（P-H4）。
// 增删轨/移轨会改变"引擎索引 == 前端轨道序号"的约定（SF2、走带音符、
// 素材调度、FX 链都只收索引），因此这类编辑仍需全量重建一次；
// 缺失插件不会阻断重建（占位在前端，P-H1）。
func (s *Supervisor) BeginPluginSession(project string, generation int64, ids []string) error {
	_, err := s.BeginPluginSessionEx(project, generation, ids)
	return err
}

// BeginPluginSessionEx 同 BeginPluginSession，额外返回"是否做了全量清空"。
// 前端据此决定是否需要重新装载全部插件（P-H2）。
func (s *Supervisor) BeginPluginSessionEx(project string, generation int64, ids []string) (bool, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return false, err
	}
	if len(ids) > plugins.MaxTracks {
		return false, fmt.Errorf("轨道数量超限（最多 %d 条）", plugins.MaxTracks)
	}
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()

	s.mu.RLock()
	oldIDs := append([]string(nil), s.trackIDs...)
	sameProject := s.sessionReady && project == s.activeProject && generation == s.projectGeneration
	s.mu.RUnlock()
	/* 引擎轨索引同时被 SF2 / 波形声部 / 走带音符 / 素材调度 / FX 链使用，
	   这些接口只收索引、没有 trackId。为了让"引擎索引 == 前端轨道序号"
	   这条既有约定保持成立，只有轨道列表**完全一致**（含顺序）时才走增量；
	   增删轨、移轨会改变后续所有轨的序号，必须全量重建映射并重载插件。
	   插件在既有轨道上的增删/换槽不改变轨道列表，走前端差分（P-H2）。 */
	sameLayout := len(oldIDs) == len(ids)
	if sameLayout {
		for i := range ids {
			if oldIDs[i] != ids[i] {
				sameLayout = false
				break
			}
		}
	}
	if sameProject && sameLayout {
		s.pluginStates.SetActiveProject(project)
		return false, nil
	}
	if sameProject {
		/* P-H2：中间插入/删除/移轨不再 plugin.clear。只对索引变化的轨道
		   卸载旧实例、按新索引重装；未变化的插件/SF2/波形/FX 链原地保留。 */
		if err := s.remapTrackLayout(cli, project, generation, oldIDs, ids); err != nil {
			return false, err
		}
		return false, nil
	}

	resp, err := cli.Request("plugin.clear", nil, 30*time.Second)
	if err != nil {
		return false, err
	}
	if err = resp.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.activeProject = project
	s.projectGeneration = generation
	s.sessionReady = true
	s.trackIDs = append([]string(nil), ids...)
	s.trackIndex = make(map[string]int, len(ids))
	for i, id := range ids {
		s.trackIndex[id] = i
	}
	s.loadedPlugins = map[string]loadedPlugin{}
	s.instrumentSlots = map[int]instrumentSlot{}
	s.status.ReplayErrors = nil
	s.status.PluginWarnings = nil
	s.status.LastError = ""
	s.lastChains = nil
	s.lastMasterChain = nil
	s.lastSoundFonts = nil
	s.lastPresets = nil
	s.lastVoices = nil
	s.lastSoundFont = ""
	s.hasMasterGain = false
	s.mu.Unlock()
	s.pluginStates.SetActiveProject(project)
	return true, nil
}

func (s *Supervisor) ResolvePluginTrack(project, trackID string, generation int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if project != s.activeProject || generation != s.projectGeneration {
		return 0, fmt.Errorf("过期的工程音频会话")
	}
	if trackID == "$master" {
		return -1, nil
	}
	if index, ok := s.trackIndex[trackID]; ok {
		return index, nil
	}
	return 0, fmt.Errorf("轨道已删除或不存在：%s", trackID)
}
func (s *Supervisor) forgetPlugin(track, slot int) {
	s.mu.Lock()
	delete(s.loadedPlugins, pluginKey(track, slot))
	if slot < 0 {
		if cur, ok := s.instrumentSlots[track]; ok && cur.Kind == "plugin" {
			delete(s.instrumentSlots, track)
		}
	}
	p := s.activeProject
	id := ""
	if track >= 0 && track < len(s.trackIDs) {
		id = s.trackIDs[track]
	}
	s.mu.Unlock()
	s.pluginStates.Drop(plugins.Target{ProjectID: p, TrackID: id, Track: track, Slot: slot})
}
