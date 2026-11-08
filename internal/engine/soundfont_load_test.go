package engine

import (
	"errors"
	"testing"
	"time"
)

func newSfLoadTestSupervisor() *Supervisor {
	s := &Supervisor{
		sfLoads:   map[int]*SoundFontLoadState{},
		sfLoadGen: map[int]int64{},
	}
	s.sfLoading.Store(1)
	s.sfLoads[0] = &SoundFontLoadState{
		Track: 0, Path: "new.sf2", Size: 1024 * 1024,
		Running: true, Phase: "loading", StartedAt: time.Now().Add(-1500 * time.Millisecond),
	}
	s.sfLoadGen[0] = 7
	return s
}

func TestSoundFontLoadCancelAndStatus(t *testing.T) {
	s := newSfLoadTestSupervisor()

	st := s.SoundFontLoadStatus(0)
	if !st.Running || st.Phase != "loading" || st.ElapsedMS < 1000 {
		t.Fatalf("unexpected running status: %+v", st)
	}
	if idle := s.SoundFontLoadStatus(3); idle.Phase != "idle" || idle.Running {
		t.Fatalf("missing track should be idle: %+v", idle)
	}

	if err := s.CancelSoundFontLoad(0); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	st = s.SoundFontLoadStatus(0)
	if !st.Cancelling || st.Phase != "cancelling" {
		t.Fatalf("cancel state not reflected: %+v", st)
	}
	if err := s.CancelSoundFontLoad(3); err == nil {
		t.Fatal("cancel on idle track should error")
	}
}

func TestSoundFontLoadFinishFailure(t *testing.T) {
	s := newSfLoadTestSupervisor()
	s.finishSoundFontLoad(0, 7, "new.sf2", "old.sf2", errors.New("parse failed"))

	st := s.SoundFontLoadStatus(0)
	if st.Running || st.Phase != "failed" || st.Error == "" {
		t.Fatalf("unexpected failed status: %+v", st)
	}
	if s.sfLoading.Load() != 0 {
		t.Fatalf("sfLoading should return to 0, got %d", s.sfLoading.Load())
	}
	// 失败不更新快照
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastSoundFonts[0] != "" {
		t.Fatalf("failed load must not update snapshot: %q", s.lastSoundFonts[0])
	}
}

func TestSoundFontLoadFinishSuccess(t *testing.T) {
	s := newSfLoadTestSupervisor()
	s.finishSoundFontLoad(0, 7, "new.sf2", "old.sf2", nil)

	st := s.SoundFontLoadStatus(0)
	if st.Running || st.Phase != "done" {
		t.Fatalf("unexpected done status: %+v", st)
	}
	s.mu.RLock()
	snapshot := s.lastSoundFonts[0]
	slot := s.instrumentSlots[0]
	s.mu.RUnlock()
	if snapshot != "new.sf2" || slot.Kind != "sf2" || slot.Path != "new.sf2" {
		t.Fatalf("snapshot not updated: %q slot=%+v", snapshot, slot)
	}
}

func TestSoundFontLoadStaleTokenIgnored(t *testing.T) {
	s := newSfLoadTestSupervisor()
	s.sfLoadGen[0] = 8 // 新请求已取代 token 7
	s.finishSoundFontLoad(0, 7, "new.sf2", "old.sf2", nil)

	st := s.SoundFontLoadStatus(0)
	if !st.Running || st.Phase != "loading" {
		t.Fatalf("stale result must be ignored: %+v", st)
	}
	if s.sfLoading.Load() != 0 {
		t.Fatalf("sfLoading should be decremented once, got %d", s.sfLoading.Load())
	}
}

func TestStartSoundFontLoadValidation(t *testing.T) {
	s := &Supervisor{sfLoads: map[int]*SoundFontLoadState{}, sfLoadGen: map[int]int64{}}
	if err := s.StartSoundFontLoad(-1, "x.sf2"); err == nil {
		t.Fatal("invalid track should be rejected")
	}
	if err := s.StartSoundFontLoad(0, ""); err == nil {
		t.Fatal("empty path should be rejected")
	}
}
