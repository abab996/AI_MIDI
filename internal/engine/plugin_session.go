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

func pluginKey(track, slot int) string { return fmt.Sprintf("%d:%d", track, slot) }
func (s *Supervisor) rememberPlugin(track, slot int, params map[string]any, ref, format string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadedPlugins == nil {
		s.loadedPlugins = map[string]loadedPlugin{}
	}
	s.loadedPlugins[pluginKey(track, slot)] = loadedPlugin{params, ref, format}
}
func (s *Supervisor) SetPluginReference(track, slot int, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pluginKey(track, slot)
	p, ok := s.loadedPlugins[key]
	if ok {
		p.Ref = ref
		s.loadedPlugins[key] = p
	}
}

// BeginPluginSession clears the previous engine project before publishing the new mapping.
func (s *Supervisor) BeginPluginSession(project string, generation int64, ids []string) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	resp, err := cli.Request("plugin.clear", nil, 30*time.Second)
	if err != nil {
		return err
	}
	if err = resp.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.activeProject = project
	s.projectGeneration = generation
	s.trackIDs = append([]string(nil), ids...)
	s.loadedPlugins = map[string]loadedPlugin{}
	s.status.ReplayErrors = nil
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
	return nil
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
	for i, id := range s.trackIDs {
		if id == trackID {
			return i, nil
		}
	}
	return 0, fmt.Errorf("轨道已删除或不存在：%s", trackID)
}
func (s *Supervisor) forgetPlugin(track, slot int) {
	s.mu.Lock()
	delete(s.loadedPlugins, pluginKey(track, slot))
	p := s.activeProject
	id := ""
	if track >= 0 && track < len(s.trackIDs) {
		id = s.trackIDs[track]
	}
	s.mu.Unlock()
	s.pluginStates.Drop(plugins.Target{ProjectID: p, TrackID: id, Track: track, Slot: slot})
}
