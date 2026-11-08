package engine

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// trackRemapPlan 旧布局 → 新布局的差分：moved 是"索引变了但轨仍在"的
// 旧索引→新索引；removed 是被删除的旧索引；newIndex 是新轨 ID→新索引。
type trackRemapPlan struct {
	newIndex map[string]int
	moved    map[int]int
	removed  []int
}

// planTrackRemap 计算轨道布局变化的纯函数（无锁、无 IO，便于单测）。
func planTrackRemap(oldIDs, ids []string) (trackRemapPlan, error) {
	plan := trackRemapPlan{
		newIndex: make(map[string]int, len(ids)),
		moved:    map[int]int{},
	}
	for i, id := range ids {
		if _, dup := plan.newIndex[id]; dup {
			return trackRemapPlan{}, fmt.Errorf("轨道 ID 重复：%s", id)
		}
		plan.newIndex[id] = i
	}
	for oldIdx, id := range oldIDs {
		if newIdx, ok := plan.newIndex[id]; ok {
			if newIdx != oldIdx {
				plan.moved[oldIdx] = newIdx
			}
			continue
		}
		plan.removed = append(plan.removed, oldIdx)
	}
	return plan, nil
}

// remapTarget 返回旧索引在新布局中的索引；轨道已删除时 false。
func (p trackRemapPlan) remapTarget(oldIDs []string, oldIdx int) (int, bool) {
	if newIdx, ok := p.moved[oldIdx]; ok {
		return newIdx, true
	}
	if oldIdx >= 0 && oldIdx < len(oldIDs) {
		if newIdx, ok := p.newIndex[oldIDs[oldIdx]]; ok && newIdx == oldIdx {
			return oldIdx, true
		}
	}
	return 0, false
}

func splitPluginKey(key string) (int, int, bool) {
	trackText, slotText, ok := strings.Cut(key, ":")
	if !ok {
		return 0, 0, false
	}
	track, err1 := strconv.Atoi(trackText)
	slot, err2 := strconv.Atoi(slotText)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return track, slot, true
}

func cloneParams(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// remapTrackLayout P-H2：中间插入/删除/移轨时不再 plugin.clear。
//
//  1. 只卸载"旧索引已失效"（被移走或删除）的插件实例；
//  2. 按新索引重装被移走的插件（保活/状态随后由 restorePluginStates 推回）；
//  3. 同步重映射 SF2/波形/预设/FX 链快照，被移走的音源在新索引重新加载；
//  4. 索引未变的插件与音源原地保留，不触发实例化。
//
// 调用方已持有 pluginQueue（与其它插件读写串行）。
func (s *Supervisor) remapTrackLayout(cli *Client, project string, generation int64, oldIDs, ids []string) error {
	plan, err := planTrackRemap(oldIDs, ids)
	if err != nil {
		return err
	}

	type unloadTarget struct{ track, slot int }
	type reloadTarget struct {
		track, slot int
		plugin      loadedPlugin
	}
	type sourceTarget struct {
		track int
		slot  instrumentSlot
	}
	var unloads []unloadTarget
	var reloads []reloadTarget
	var sourceReloads []sourceTarget

	s.mu.Lock()
	// ① 插件：旧索引失效的先卸载；被移走的按新索引排队重装
	for key, p := range s.loadedPlugins {
		track, slot, ok := splitPluginKey(key)
		if !ok || track < 0 {
			continue // 主总线（-1）不随轨序变化
		}
		newTrack, keep := plan.remapTarget(oldIDs, track)
		if !keep {
			unloads = append(unloads, unloadTarget{track, slot})
			delete(s.loadedPlugins, key)
			continue
		}
		if newTrack == track {
			continue // 索引没变：实例原地保留
		}
		unloads = append(unloads, unloadTarget{track, slot})
		params := cloneParams(p.Params)
		params["track"] = newTrack
		delete(params, "master")
		reloads = append(reloads, reloadTarget{
			track: newTrack, slot: slot,
			plugin: loadedPlugin{Params: params, Ref: p.Ref, Format: p.Format},
		})
		delete(s.loadedPlugins, key)
	}
	for _, r := range reloads {
		s.loadedPlugins[pluginKey(r.track, r.slot)] = r.plugin
	}

	// ② 每轨乐器槽（SF2/波形/插件乐器）重映射；被移走的 SF2/波形需在新索引重放
	newSlots := make(map[int]instrumentSlot, len(s.instrumentSlots))
	for oldIdx, slot := range s.instrumentSlots {
		newIdx, keep := plan.remapTarget(oldIDs, oldIdx)
		if !keep {
			continue
		}
		if slot.Params != nil {
			slot.Params = cloneParams(slot.Params)
			slot.Params["track"] = newIdx
		}
		newSlots[newIdx] = slot
		if newIdx != oldIdx && (slot.Kind == "sf2" || slot.Kind == "wave") {
			sourceReloads = append(sourceReloads, sourceTarget{track: newIdx, slot: slot})
		}
	}
	s.instrumentSlots = newSlots

	// ③ 旧的三张兼容表同步重映射
	remapFonts := make(map[int]string, len(s.lastSoundFonts))
	for oldIdx, path := range s.lastSoundFonts {
		if newIdx, keep := plan.remapTarget(oldIDs, oldIdx); keep {
			remapFonts[newIdx] = path
		}
	}
	s.lastSoundFonts = remapFonts
	s.lastSoundFont = remapFonts[0]

	remapPresets := make(map[int][2]int, len(s.lastPresets))
	for oldIdx, bp := range s.lastPresets {
		if newIdx, keep := plan.remapTarget(oldIDs, oldIdx); keep {
			remapPresets[newIdx] = bp
		}
	}
	s.lastPresets = remapPresets

	remapVoices := make(map[int]map[string]any, len(s.lastVoices))
	for oldIdx, voice := range s.lastVoices {
		if newIdx, keep := plan.remapTarget(oldIDs, oldIdx); keep {
			remapVoices[newIdx] = voice
		}
	}
	s.lastVoices = remapVoices

	remapChains := make(map[int][]map[string]any, len(s.lastChains))
	for oldIdx, chain := range s.lastChains {
		if newIdx, keep := plan.remapTarget(oldIDs, oldIdx); keep {
			remapChains[newIdx] = chain
		}
	}
	s.lastChains = remapChains

	// ④ 发布新映射
	s.activeProject = project
	s.projectGeneration = generation
	s.sessionReady = true
	s.trackIDs = append([]string(nil), ids...)
	s.trackIndex = plan.newIndex
	s.mu.Unlock()

	// ⑤ 先卸载所有失效实例，避免"新索引被旧实例占用"的碰撞
	for _, u := range unloads {
		resp, reqErr := cli.Request("plugin.unload", pluginTarget(u.track, u.slot), 30*time.Second)
		if reqErr == nil {
			reqErr = resp.Err()
		}
		if reqErr != nil {
			select {
			case <-cli.Done():
				return reqErr
			default:
				slog.Warn("[engine] 轨道重映射卸载插件失败", "track", u.track, "slot", u.slot, "err", reqErr)
			}
		}
	}

	// ⑥ 按新索引重装被移走的插件
	for _, r := range reloads {
		resp, reqErr := cli.Request("plugin.load", r.plugin.Params, 60*time.Second)
		if reqErr == nil {
			reqErr = resp.Err()
		}
		if reqErr != nil {
			s.recordPluginWarning(r.plugin.Ref, reqErr)
			slog.Warn("[engine] 轨道重映射重装插件失败", "track", r.track, "slot", r.slot, "ref", r.plugin.Ref, "err", reqErr)
		}
	}

	// ⑦ 被移走的 SF2/波形音源在新索引重放（引擎池已缓存，通常很快）
	for _, src := range sourceReloads {
		switch src.slot.Kind {
		case "sf2":
			if src.slot.Path == "" {
				continue
			}
			if err := s.loadSoundFontWithTrackTimeout(cli, src.track, src.slot.Path, 2*time.Minute); err != nil {
				slog.Warn("[engine] 轨道重映射重放音色失败", "track", src.track, "path", src.slot.Path, "err", err)
				continue
			}
			if src.slot.HasPre {
				if resp, err := cli.Request("setTrackPreset", map[string]any{
					"track": src.track, "bank": src.slot.Preset[0], "program": src.slot.Preset[1],
				}, 10*time.Second); err != nil || resp.Err() != nil {
					if err == nil {
						err = resp.Err()
					}
					slog.Warn("[engine] 轨道重映射重放预设失败", "track", src.track, "err", err)
				}
			}
		case "wave":
			params := cloneParams(src.slot.Voice)
			params["track"] = src.track
			if resp, err := cli.Request("setTrackVoice", params, 10*time.Second); err != nil || resp.Err() != nil {
				if err == nil {
					err = resp.Err()
				}
				slog.Warn("[engine] 轨道重映射重放波形声部失败", "track", src.track, "err", err)
			}
		}
	}

	// ⑧ 重装后的插件把保存的状态推回去（按 trackID 匹配，索引变化无关）
	s.restorePluginStates(cli)
	s.pluginStates.SetActiveProject(project)
	return nil
}
