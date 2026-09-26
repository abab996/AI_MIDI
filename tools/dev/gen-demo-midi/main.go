// gen-demo-midi 生成内置示例工程用的 demo.mid（小星星第一乐句 + 和声）。
// 运行: go run ./tools/dev/gen-demo-midi > 内部不落盘，直接写目标路径参数
package main

import (
	"fmt"
	"os"
	"strings"

	"aimidi/internal/midi"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gen-demo-midi <output.mid>")
		os.Exit(1)
	}

	var b strings.Builder
	note := func(name string, vel, start, end int) {
		fmt.Fprintf(&b, "[note: %q, velocity: %q, start: %q, end: %q ]\n",
			name, fmt.Sprint(vel), fmt.Sprint(start), fmt.Sprint(end))
	}

	// 旋律：小星星第一乐句（C4-C4-G4-G4-A4-A4-G4- | F4-F4-E4-E4-D4-D4-C4-）
	melody := []struct {
		name  string
		start int
		end   int
	}{
		{"C4", 0, 1}, {"C4", 1, 2}, {"G4", 2, 3}, {"G4", 3, 4},
		{"A4", 4, 5}, {"A4", 5, 6}, {"G4", 6, 8},
		{"F4", 8, 9}, {"F4", 9, 10}, {"E4", 10, 11}, {"E4", 11, 12},
		{"D4", 12, 13}, {"D4", 13, 14}, {"C4", 14, 16},
	}
	for _, m := range melody {
		note(m.name, 95, m.start, m.end)
	}

	// 和声层：低音区三和弦（C / C / F / G→C），力度收敛不抢旋律
	chords := []struct {
		start, end int
		names      []string
	}{
		{0, 4, []string{"C3", "E3", "G3"}},
		{4, 8, []string{"C3", "E3", "G3"}},
		{8, 12, []string{"F3", "A3", "C4"}},
		{12, 14, []string{"G2", "B2", "D3"}},
		{14, 16, []string{"C3", "E3", "G3"}},
	}
	for _, c := range chords {
		for _, n := range c.names {
			note(n, 50, c.start, c.end)
		}
	}

	if err := midi.OutNote(b.String(), 100, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "已生成", os.Args[1])
}
