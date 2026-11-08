package engine

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuildScanArgs(t *testing.T) {
	args := buildScanArgs("out.json", "cache.xml", []string{`C:\VST3`, `D:\Plugins`})
	joined := strings.Join(args, " ")
	for _, needle := range []string{"--scan-only", "--out out.json", "--cache cache.xml", `--dirs C:\VST3;D:\Plugins`} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %q in %q", needle, joined)
		}
	}
	if got := buildScanArgs("o", "c", nil); len(got) != 5 {
		t.Fatalf("no-dirs args should have 5 items, got %v", got)
	}
}

func TestParseScanOutput(t *testing.T) {
	list, err := parseScanOutput([]byte(`{"count":1,"plugins":[{"id":"VST3|a|1","name":"A","path":"a.vst3","instrument":true}],"formats":["VST3"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Plugins) != 1 || list.Plugins[0].Name != "A" || !list.Plugins[0].Instrument {
		t.Fatalf("unexpected list: %+v", list)
	}
	// count 缺失时按 plugins 长度补齐
	list, err = parseScanOutput([]byte(`{"plugins":[{"id":"x"},{"id":"y"}]}`))
	if err != nil || list.Count != 2 {
		t.Fatalf("count fallback failed: %+v err=%v", list, err)
	}
	if _, err := parseScanOutput(nil); err == nil {
		t.Fatal("empty output should fail")
	}
	if _, err := parseScanOutput([]byte("not json")); err == nil {
		t.Fatal("invalid output should fail")
	}
}

// TestScanHelperProcess 作为 runScanProcess 的子进程替身。
func TestScanHelperProcess(t *testing.T) {
	mode := os.Getenv("MM_SCAN_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "sleep":
		time.Sleep(5 * time.Second)
	case "crash":
		os.Exit(3)
	case "ok":
		os.Exit(0)
	}
}

func TestRunScanProcessCrash(t *testing.T) {
	t.Setenv("MM_SCAN_HELPER", "crash")
	_, err := runScanProcess(os.Args[0], []string{"-test.run=TestScanHelperProcess"}, 10*time.Second)
	if err == nil {
		t.Fatal("crashing scanner should return an error")
	}
	if errors.Is(err, errScanTimeout) {
		t.Fatal("crash must not be reported as timeout")
	}
}

func TestRunScanProcessTimeout(t *testing.T) {
	t.Setenv("MM_SCAN_HELPER", "sleep")
	_, err := runScanProcess(os.Args[0], []string{"-test.run=TestScanHelperProcess"}, 300*time.Millisecond)
	if !errors.Is(err, errScanTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestRunScanProcessSuccess(t *testing.T) {
	t.Setenv("MM_SCAN_HELPER", "ok")
	if _, err := runScanProcess(os.Args[0], []string{"-test.run=TestScanHelperProcess"}, 10*time.Second); err != nil {
		t.Fatalf("helper success failed: %v", err)
	}
}
