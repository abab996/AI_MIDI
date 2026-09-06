package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// 中文 locale 下 go-runewidth 会把 box-drawing 等歧义宽度字符按 2 列计算，
// 但终端实际渲染为 1 列，这里强制关闭以保证对齐。
func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
}

func main() {
	rootFlag := flag.String("root", "", "项目根目录（默认取 PROJ_ROOT 环境变量或当前目录）")
	preview := flag.Bool("preview", false, "非交互预览：直接渲染最终画面后退出")
	tabFlag := flag.Int("tab", 0, "预览模式的图表页：0=模块 1=语言")
	flag.Parse()

	root := *rootFlag
	if root == "" {
		if env := os.Getenv("PROJ_ROOT"); env != "" {
			root = env
		} else {
			root, _ = os.Getwd()
		}
	}
	root = strings.TrimRight(root, "\\/")

	if *preview {
		s, err := Scan(root, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "扫描失败:", err)
			os.Exit(1)
		}
		m := newModel(root)
		m.stats = s
		m.phase = phaseReady
		m.animP = 1
		m.w, m.h = 96, 80
		m.tab = *tabFlag
		if m.groupByName(m.selected) == nil {
			m.selected = m.defaultSelected()
		}
		lines, _ := m.buildLayout()
		fmt.Println(strings.Join(lines, "\n"))
		return
	}

	m := newModel(root)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI 启动失败:", err)
		os.Exit(1)
	}
}
