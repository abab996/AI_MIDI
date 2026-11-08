package engine

import "testing"

func TestPlanTrackRemap(t *testing.T) {
	old := []string{"a", "b", "c"}
	cases := []struct {
		name    string
		ids     []string
		moved   map[int]int
		removed []int
	}{
		{"same", []string{"a", "b", "c"}, map[int]int{}, nil},
		{"append", []string{"a", "b", "c", "d"}, map[int]int{}, nil},
		{"trim", []string{"a", "b"}, map[int]int{}, []int{2}},
		{"insert-middle", []string{"a", "x", "b", "c"}, map[int]int{1: 2, 2: 3}, nil},
		{"remove-middle", []string{"a", "c"}, map[int]int{2: 1}, []int{1}},
		{"move", []string{"b", "a", "c"}, map[int]int{0: 1, 1: 0}, nil},
		{"reverse", []string{"c", "b", "a"}, map[int]int{0: 2, 2: 0}, nil},
	}
	for _, c := range cases {
		plan, err := planTrackRemap(old, c.ids)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(plan.moved) != len(c.moved) {
			t.Fatalf("%s: moved=%v want %v", c.name, plan.moved, c.moved)
		}
		for k, v := range c.moved {
			if plan.moved[k] != v {
				t.Fatalf("%s: moved[%d]=%d want %d", c.name, k, plan.moved[k], v)
			}
		}
		if len(plan.removed) != len(c.removed) {
			t.Fatalf("%s: removed=%v want %v", c.name, plan.removed, c.removed)
		}
		for i := range c.removed {
			if plan.removed[i] != c.removed[i] {
				t.Fatalf("%s: removed=%v want %v", c.name, plan.removed, c.removed)
			}
		}
		for i, id := range c.ids {
			if plan.newIndex[id] != i {
				t.Fatalf("%s: newIndex[%s]=%d want %d", c.name, id, plan.newIndex[id], i)
			}
		}
	}
}

func TestPlanTrackRemapDuplicate(t *testing.T) {
	if _, err := planTrackRemap([]string{"a"}, []string{"a", "a"}); err == nil {
		t.Fatal("duplicate track ids must be rejected")
	}
}

func TestRemapTarget(t *testing.T) {
	old := []string{"a", "b", "c", "d"}
	plan, err := planTrackRemap(old, []string{"d", "b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	// d: 3→0；b: 1→1 不变；a: 0→2；c 被删除
	cases := map[int]struct {
		want int
		keep bool
	}{
		0: {2, true},
		1: {1, true},
		2: {0, false},
		3: {0, true},
	}
	for oldIdx, c := range cases {
		got, keep := plan.remapTarget(old, oldIdx)
		if keep != c.keep || (keep && got != c.want) {
			t.Fatalf("old=%d: got (%d,%v) want (%d,%v)", oldIdx, got, keep, c.want, c.keep)
		}
	}
}
