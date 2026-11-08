package engine

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPluginEventRing(t *testing.T) {
	s := &Supervisor{}
	cli := &Client{dead: make(chan struct{}), events: make(chan Event, 8)}
	go s.consumeClientEvents(cli)

	cli.events <- Event{Name: "plugin.params", Data: json.RawMessage(`{"track":0,"slot":0,"changes":[{"index":1,"value":0.5}]}`)}
	cli.events <- Event{Name: "plugin.params", Data: json.RawMessage(`{"track":1,"slot":-1,"changes":[{"index":2,"value":0.7}]}`)}

	deadline := time.Now().Add(2 * time.Second)
	var seq int64
	var events []PluginParamEvent
	for time.Now().Before(deadline) {
		seq, events = s.PluginEvents(0)
		if len(events) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if seq != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected seq: total=%d first=%d second=%d", seq, events[0].Seq, events[1].Seq)
	}
	if events[0].Event != "plugin.params" {
		t.Fatalf("unexpected event name %q", events[0].Event)
	}

	// after 游标只返回更新的事件
	_, newer := s.PluginEvents(1)
	if len(newer) != 1 || newer[0].Seq != 2 {
		t.Fatalf("after-filter failed: %+v", newer)
	}
	close(cli.dead)
}
