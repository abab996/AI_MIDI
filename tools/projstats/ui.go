package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// ---------------- 颜色工具 ----------------

func hexRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return 255, 255, 255
	}
	v, _ := strconv.ParseInt(h, 16, 64)
	return int(v>>16) & 0xFF, int(v>>8) & 0xFF, int(v & 0xFF)
}

func blend(a, b string, t float64) string {
	r1, g1, b1 := hexRGB(a)
	r2, g2, b2 := hexRGB(b)
	mix := func(x, y int) int { return int(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return fmt.Sprintf("#%02X%02X%02X", mix(r1, r2), mix(g1, g2), mix(b1, b2))
}

func dimColor(c string) string { return blend(c, "#14161B", 0.72) }

func bgSeq(hex string) string {
	r, g, b := hexRGB(hex)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func fgSeq(hex string) string {
	r, g, b := hexRGB(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

const reset = "\x1b[0m"

// ---------------- 画布（含鼠标命中区域登记） ----------------

type region struct {
	y, x0, x1 int
	id        string
}

type canvas struct {
	lines   []string
	regions []region
	cur     strings.Builder
	curW    int
	maxW    int
}

func newCanvas(maxW int) *canvas { return &canvas{maxW: maxW} }

// add 追加一段：plain 用于宽度/截断计算，render 用于最终着色（nil 则原样），id 非空则登记为可点击区域
func (c *canvas) add(plain, id string, render func(string) string) {
	if c.maxW <= 0 {
		return
	}
	avail := c.maxW - c.curW
	if avail <= 0 {
		return
	}
	if runewidth.StringWidth(plain) > avail {
		plain = runewidth.Truncate(plain, max(0, avail-1), "..")
	}
	w := runewidth.StringWidth(plain)
	if w == 0 {
		return
	}
	if id != "" {
		c.regions = append(c.regions, region{y: len(c.lines), x0: c.curW, x1: c.curW + w, id: id})
	}
	if render != nil {
		c.cur.WriteString(render(plain))
	} else {
		c.cur.WriteString(plain)
	}
	c.curW += w
}

func (c *canvas) addStyled(plain, style, id string) {
	c.add(plain, id, func(s string) string { return style + s + reset })
}

func (c *canvas) newline() {
	c.lines = append(c.lines, c.cur.String())
	c.cur.Reset()
	c.curW = 0
}

// ---------------- 动画辅助 ----------------

func easeOutCubic(t float64) float64 {
	t = clamp(t, 0, 1)
	return 1 - math.Pow(1-t, 3)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sub 返回动画主进度 p 在 [start, start+dur] 区间的局部进度
func sub(p, start, dur float64) float64 { return clamp((p-start)/dur, 0, 1) }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------- 模型 ----------------

type phase int

const (
	phaseScanning phase = iota
	phaseReveal
	phaseReady
)

type animTickMsg struct{}
type spinTickMsg struct{}
type scanProgressMsg struct{ files int }
type scanDoneMsg struct {
	stats *Stats
	err   error
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

var heatRunes = []rune("▁▂▃▄▅▆▇█")

type model struct {
	root string
	w, h int

	phase      phase
	spinnerIdx int
	filesFound int

	stats   *Stats
	scanCh  chan int
	scanRes chan scanDoneMsg
	scanErr error

	animP float64 // 0..1 主动画进度
	distP float64 // 0..1 分布条独立动画进度（切页签时重放）

	tab       int // 0=模块 1=语言
	selected  string
	fileRel   string
	hover     string
	scrollY   int
	debug     bool
	lastMouse string
}

func newModel(root string) *model {
	return &model{root: root}
}

// ---------------- 消息与命令 ----------------

func spinTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return spinTickMsg{} })
}

func animTick() tea.Cmd {
	return tea.Tick(33*time.Millisecond, func(time.Time) tea.Msg { return animTickMsg{} })
}

func startScan(root string) (chan int, chan scanDoneMsg) {
	ch := make(chan int, 8)
	res := make(chan scanDoneMsg, 1)
	go func() {
		s, err := Scan(root, ch)
		close(ch)
		res <- scanDoneMsg{stats: s, err: err}
		close(res)
	}()
	return ch, res
}

func waitScan(res chan scanDoneMsg) tea.Cmd {
	return func() tea.Msg { return <-res }
}

func waitProgress(ch chan int) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-ch
		if !ok {
			return nil
		}
		return scanProgressMsg{files: n}
	}
}

func (m *model) Init() tea.Cmd {
	m.scanCh, m.scanRes = startScan(m.root)
	return tea.Batch(spinTick(), waitProgress(m.scanCh), waitScan(m.scanRes))
}

func (m *model) rescan() tea.Cmd {
	m.phase = phaseScanning
	m.animP = 0
	m.stats = nil
	m.scanErr = nil
	m.filesFound = 0
	m.scrollY = 0
	m.fileRel = ""
	m.scanCh, m.scanRes = startScan(m.root)
	return tea.Batch(spinTick(), waitProgress(m.scanCh), waitScan(m.scanRes))
}

// ---------------- 分组查询 ----------------

func (m *model) groups() []*GroupStat {
	if m.stats == nil {
		return nil
	}
	if m.tab == 0 {
		return m.stats.Mods
	}
	return m.stats.Langs
}

func (m *model) groupByName(name string) *GroupStat {
	if m.stats == nil {
		return nil
	}
	for _, g := range m.stats.Mods {
		if g.Name == name {
			return g
		}
	}
	for _, g := range m.stats.Langs {
		if g.Name == name {
			return g
		}
	}
	return nil
}

func (m *model) defaultSelected() string {
	gs := m.groups()
	if len(gs) == 0 {
		return ""
	}
	best := gs[0]
	for _, g := range gs[1:] {
		if g.Code > best.Code {
			best = g
		}
	}
	return best.Name
}

// ---------------- 更新 ----------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clampScroll()
		return m, nil

	case spinTickMsg:
		if m.phase == phaseScanning {
			m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)
			return m, spinTick()
		}
		return m, nil

	case scanProgressMsg:
		m.filesFound = msg.files
		return m, waitProgress(m.scanCh)

	case scanDoneMsg:
		if msg.err != nil {
			m.scanErr = msg.err
			return m, nil
		}
		m.stats = msg.stats
		m.phase = phaseReveal
		m.animP = 0
		m.distP = 0
		if m.selected == "" || m.groupByName(m.selected) == nil {
			m.selected = m.defaultSelected()
		}
		m.clampScroll()
		return m, animTick()

	case animTickMsg:
		if m.phase == phaseReveal {
			m.animP += 0.028
			if m.animP >= 1 {
				m.animP = 1
				m.phase = phaseReady
			}
			return m, animTick()
		}
		if m.distP < 1 {
			m.distP += 0.05
			if m.distP > 1 {
				m.distP = 1
			}
			return m, animTick()
		}
		return m, nil

	case tea.MouseMsg:
		return m, m.onMouse(msg)

	case tea.KeyMsg:
		return m, m.onKey(msg)
	}
	return m, nil
}

// replayDist 重放分布条动画（切页签时调用）
func (m *model) replayDist() tea.Cmd {
	m.distP = 0
	return animTick()
}

func (m *model) onKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	case "d":
		m.debug = !m.debug
	case "r":
		return m.rescan()
	case "1":
		m.tab = 0
		m.fileRel = ""
		if m.groupByName(m.selected) == nil {
			m.selected = m.defaultSelected()
		}
		return m.replayDist()
	case "2":
		m.tab = 1
		m.fileRel = ""
		if m.groupByName(m.selected) == nil {
			m.selected = m.defaultSelected()
		}
		return m.replayDist()
	case "left":
		m.tab = 0
		m.fileRel = ""
		return m.replayDist()
	case "right":
		m.tab = 1
		m.fileRel = ""
		return m.replayDist()
	case "tab":
		gs := m.groups()
		if len(gs) > 0 {
			idx := -1
			for i, g := range gs {
				if g.Name == m.selected {
					idx = i
					break
				}
			}
			m.selected = gs[(idx+1+len(gs))%len(gs)].Name
			m.fileRel = ""
		}
	case "up", "k":
		m.scrollY -= 3
		m.clampScroll()
	case "down", "j":
		m.scrollY += 3
		m.clampScroll()
	case "pgup":
		m.scrollY -= m.h / 2
		m.clampScroll()
	case "pgdown":
		m.scrollY += m.h / 2
		m.clampScroll()
	case "enter":
		if m.hover != "" {
			m.applyHit(m.hover)
			m.clampScroll()
		}
	case "backspace":
		m.fileRel = ""
	}
	return nil
}

func (m *model) onMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scrollY -= 3
		m.clampScroll()
		return nil
	case tea.MouseButtonWheelDown:
		m.scrollY += 3
		m.clampScroll()
		return nil
	}
	if msg.Action == tea.MouseActionMotion {
		m.hover = m.hitTest(msg.X, msg.Y)
		return nil
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		id := m.hitTest(msg.X, msg.Y)
		m.lastMouse = fmt.Sprintf("(%d,%d)->%q", msg.X, msg.Y, id)
		if id != "" {
			cmd := m.applyHit(id)
			m.clampScroll()
			return cmd
		}
	}
	return nil
}

func (m *model) applyHit(id string) tea.Cmd {
	switch {
	case strings.HasPrefix(id, "tab:"):
		n, _ := strconv.Atoi(id[4:])
		m.tab = n
		m.fileRel = ""
		if m.groupByName(m.selected) == nil {
			m.selected = m.defaultSelected()
		}
		return m.replayDist()
	case strings.HasPrefix(id, "grp:"):
		m.selected = id[4:]
		m.fileRel = ""
		return m.replayDist()
	case strings.HasPrefix(id, "file:"):
		m.fileRel = id[5:]
	}
	return nil
}

func (m *model) visibleH() int {
	h := m.h - 1 // 底部提示栏
	if m.debug {
		h--
	}
	if h < 3 {
		h = 3
	}
	return h
}

func (m *model) clampScroll() {
	lines, _ := m.buildLayout()
	maxScroll := len(lines) - m.visibleH()
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollY > maxScroll {
		m.scrollY = maxScroll
	}
	if m.scrollY < 0 {
		m.scrollY = 0
	}
}

func (m *model) hitTest(x, y int) string {
	_, regions := m.buildLayout()
	cy := y + m.scrollY
	for _, r := range regions {
		if r.y == cy && x >= r.x0 && x < r.x1 {
			return r.id
		}
	}
	return ""
}

// ---------------- 视图 ----------------

func (m *model) View() string {
	if m.w <= 0 {
		return "正在启动..."
	}
	lines, regions := m.buildLayout()
	vis := m.visibleH()
	lo := m.scrollY
	if lo > len(lines) {
		lo = len(lines)
	}
	hi := min(lo+vis, len(lines))
	out := append([]string{}, lines[lo:hi]...)
	for len(out) < vis {
		out = append(out, "")
	}
	out = append(out, m.footer(len(lines)))
	if m.debug {
		out = append(out, fmt.Sprintf("DEBUG 尺寸=%dx%d 内容行=%d 命中区=%d 滚动=%d 悬停=%q 最近点击=%s",
			m.w, m.h, len(lines), len(regions), m.scrollY, m.hover, m.lastMouse))
	}
	return strings.Join(out, "\n")
}

func (m *model) footer(totalLines int) string {
	if m.scanErr != nil && m.stats == nil {
		return "\x1b[91m扫描失败: " + m.scanErr.Error() + "  ·  按 R 重试 / Q 退出" + reset
	}
	pct := 100
	if totalLines > m.visibleH() {
		pct = (m.scrollY + m.visibleH()) * 100 / totalLines
	}
	left := " 点击图例/色块查看明细 · 1/2 或 ←→ 切换图表 · Tab 下一项 · 滚轮滚动 · R 重新扫描 · D 调试 · Q 退出"
	if m.debug {
		left = " 调试模式已开启（按 D 关闭）"
	}
	right := fmt.Sprintf("%3d%%", pct)
	left = cut(left, m.w-8)
	pad := m.w - runewidth.StringWidth(left) - len(right) - 1
	if pad < 1 {
		pad = 1
	}
	return "\x1b[90m" + left + strings.Repeat(" ", pad) + right + reset
}

// buildLayout 构建完整内容行与命中区域（Update 与 View 共用，保证确定性）
func (m *model) buildLayout() ([]string, []region) {
	cw := m.w - 2
	if cw > 84 {
		cw = 84
	}
	if cw < 40 {
		cw = 40
	}
	c := newCanvas(cw)

	m.drawBanner(c)

	if m.scanErr != nil && m.stats == nil {
		c.newline()
		c.addStyled(" 扫描失败: "+m.scanErr.Error(), "\x1b[91m", "")
		c.newline()
		return c.lines, c.regions
	}
	if m.stats == nil {
		c.newline()
		sp := "\x1b[96m" + string(spinnerFrames[m.spinnerIdx%len(spinnerFrames)]) + reset
		c.add("●", "", func(string) string { return sp })
		c.add(" 正在扫描项目  ", "", nil)
		c.addStyled(strconv.Itoa(m.filesFound), "\x1b[1m\x1b[97m", "")
		c.add(fmt.Sprintf(" 个文件 / %d 个目录 ...", m.filesFound/6), "", nil)
		c.newline()
		return c.lines, c.regions
	}

	p := easeOutCubic(m.animP)

	if p > 0.06 {
		m.drawOverview(c)
	}
	if p > 0.16 {
		m.drawActivity(c, p)
	}
	if p > 0.26 {
		m.drawDistribution(c, p)
	}
	if p > 0.52 {
		m.drawTopFiles(c, p)
	}
	if p > 0.78 {
		m.drawMetrics(c)
	}
	return c.lines, c.regions
}

// ---------------- 各板块绘制 ----------------

func (m *model) drawBanner(c *canvas) {
	bw := c.maxW
	inner := bw - 2
	c.addStyled("╭"+strings.Repeat("─", inner)+"╮", "\x1b[96m", "")
	c.newline()

	title := "🎵 AI_MIDI-go 项目全景统计报告"
	l := max(0, (inner-runewidth.StringWidth(title))/2)
	c.addStyled("│", "\x1b[96m", "")
	c.add(strings.Repeat(" ", l), "", nil)
	c.addStyled("🎵 AI_MIDI-go ", "\x1b[1m\x1b[96m", "")
	c.addStyled("项目全景统计报告", "\x1b[1m\x1b[97m", "")
	c.add(strings.Repeat(" ", max(0, inner-l-runewidth.StringWidth(title))), "", nil)
	c.addStyled("│", "\x1b[96m", "")
	c.newline()

	sub := "Go (Wails) + C++/JUCE 音频引擎 + Web 前端"
	tag := ""
	if m.stats != nil && m.stats.Tag != "" {
		tag = m.stats.Tag + " | "
	}
	full := tag + sub
	l2 := max(0, (inner-runewidth.StringWidth(full))/2)
	c.addStyled("│", "\x1b[96m", "")
	c.add(strings.Repeat(" ", l2), "", nil)
	if tag != "" {
		c.addStyled(tag, "\x1b[92m", "")
	}
	c.addStyled(sub, "\x1b[37m", "")
	c.add(strings.Repeat(" ", max(0, inner-l2-runewidth.StringWidth(full))), "", nil)
	c.addStyled("│", "\x1b[96m", "")
	c.newline()
	c.addStyled("╰"+strings.Repeat("─", inner)+"╯", "\x1b[96m", "")
	c.newline()
}

func (m *model) section(c *canvas, title string) {
	tw := runewidth.StringWidth(" " + title + " ")
	rule := max(2, (c.maxW-2-tw)/2)
	c.newline()
	c.add("  ", "", nil)
	c.addStyled(strings.Repeat("─", rule), "\x1b[90m", "")
	c.addStyled(" "+title+" ", "\x1b[1m\x1b[96m", "")
	c.addStyled(strings.Repeat("─", max(2, c.maxW-2-rule-tw)), "\x1b[90m", "")
	c.newline()
}

func num(v int) string {
	s := strconv.Itoa(v)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func pad(s string, n int) string {
	d := n - runewidth.StringWidth(s)
	if d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padL2(s string, n int) string {
	d := n - runewidth.StringWidth(s)
	if d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

func cut(s string, n int) string {
	if n <= 1 {
		return ""
	}
	if runewidth.StringWidth(s) <= n {
		return s
	}
	return runewidth.Truncate(s, n-1, "..")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pctOf(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func pctSize(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func (m *model) drawOverview(c *canvas) {
	s := m.stats
	m.section(c, "一 · 项目概览")
	type kv struct{ k, v string }
	var rows []kv
	addKV := func(k, v string) { rows = append(rows, kv{k, v}) }

	span, first, last := "-", "-", "-"
	if !s.FirstTime.IsZero() {
		span = fmt.Sprintf("%d 天", int(time.Since(s.FirstTime).Hours()/24))
		first = s.FirstTime.Format("2006/01/02")
	}
	if !s.LastTime.IsZero() {
		last = s.LastTime.Format("2006/01/02 15:04")
	}
	if s.HasGit {
		addKV("分支", s.Branch)
		addKV("最新标签", orDash(s.Tag))
		addKV("最新提交", orDash(s.Hash))
		addKV("提交总数", fmt.Sprintf("%d 次", s.TotalCommits))
		addKV("首次提交", first)
		addKV("项目跨度", span)
		addKV("近 7 天", fmt.Sprintf("%d 次", s.C7))
		addKV("近 30 天", fmt.Sprintf("%d 次", s.C30))
		if s.Dirty > 0 {
			addKV("未提交变更", fmt.Sprintf("%d 个文件", s.Dirty))
		} else {
			addKV("未提交变更", "工作区干净")
		}
		addKV("贡献者", fmt.Sprintf("%d 人", s.NContrib))
	} else {
		addKV("Git", "未检测到 git")
	}
	addKV("工作区大小", SizeStr(s.WorkSize))
	addKV(".git 大小", SizeStr(s.GitSize))
	addKV("总行数", fmt.Sprintf("%s 行", num(s.Totals.Total)))
	addKV("纯代码行", fmt.Sprintf("%s 行", num(s.Totals.Code)))
	addKV("代码文件体积", SizeStr(s.Totals.Size))
	addKV("占工作区", fmt.Sprintf("%.1f%%", pctSize(s.Totals.Size, s.WorkSize)))
	if s.GoVer != "" {
		addKV("Go 版本", s.GoVer)
		addKV("Go 依赖", fmt.Sprintf("%d 个", s.GoDeps))
	}
	half := (len(rows) + 1) / 2
	for i := 0; i < half; i++ {
		c.newline()
		c.add("  ", "", nil)
		c.addStyled(pad(rows[i].k, 14), "\x1b[90m", "")
		c.addStyled(pad(rows[i].v, 26), "\x1b[97m", "")
		if j := i + half; j < len(rows) {
			c.addStyled(pad(rows[j].k, 14), "\x1b[90m", "")
			c.addStyled(rows[j].v, "\x1b[97m", "")
		}
	}
	if s.HasGit && s.LastMsg != "" {
		c.newline()
		c.add("  ", "", nil)
		c.addStyled(pad("最后动态", 14), "\x1b[90m", "")
		c.addStyled(cut(s.Hash+"  "+last+"  "+s.LastAuthor, c.maxW-16), "\x1b[97m", "")
		c.newline()
		c.add("  ", "", nil)
		c.add(pad("", 12), "", nil)
		c.addStyled(cut(s.LastMsg, c.maxW-16), "\x1b[90m", "")
	}
}

func (m *model) drawActivity(c *canvas, p float64) {
	s := m.stats
	m.section(c, "二 · 提交活跃度")
	lp := sub(p, 0.16, 0.25)

	c.newline()
	c.add("  ", "", nil)
	c.addStyled("近 26 周提交热度（共 ", "\x1b[90m", "")
	c.addStyled(strconv.Itoa(s.TotalCommits), "\x1b[97m", "")
	c.addStyled(" 次，峰值 ", "\x1b[90m", "")
	c.addStyled(strconv.Itoa(s.WeekMax), "\x1b[97m", "")
	c.addStyled(" 次/周）", "\x1b[90m", "")
	c.newline()
	c.add("  ", "", nil)
	shown := int(math.Round(26 * lp))
	for i, v := range s.Buckets {
		if i >= shown || v <= 0 {
			c.addStyled("▁", "\x1b[90m", "")
			continue
		}
		idx := int(math.Ceil(float64(v) * 7 / math.Max(1, float64(s.WeekMax))))
		idx = clampInt(idx, 1, 7)
		col := "\x1b[96m"
		if idx >= 5 {
			col = "\x1b[93m"
		} else if idx >= 3 {
			col = "\x1b[92m"
		}
		c.addStyled(string(heatRunes[idx]), col, "")
	}
	c.newline()
	c.add("  ", "", nil)
	c.addStyled(time.Now().AddDate(0, 0, -181).Format("2006/01/02"), "\x1b[90m", "")
	c.add(strings.Repeat(" ", 16), "", nil)
	c.addStyled("今天", "\x1b[90m", "")
	c.newline()

	if len(s.Contributors) > 0 {
		c.newline()
		c.add("  ", "", nil)
		c.addStyled("贡献者排行", "\x1b[90m", "")
		c.newline()
		maxC := s.Contributors[0].Count
		for i, cg := range s.Contributors {
			fill := clampInt(int(math.Round(14*float64(cg.Count)/math.Max(1, float64(maxC))*lp)), 1, 14)
			c.newline()
			c.add("  ", "", nil)
			c.addStyled(padL2(strconv.Itoa(i+1), 2), "\x1b[93m", "")
			c.add(". ", "", nil)
			c.addStyled(pad(cut(cg.Name, 14), 24), "\x1b[97m", "")
			c.addStyled(padL2(fmt.Sprintf("%d 次", cg.Count), 6), "\x1b[90m", "")
			c.add("  ", "", nil)
			c.addStyled(strings.Repeat("█", fill), "\x1b[92m", "")
			c.addStyled(strings.Repeat("░", max(0, 14-fill)), "\x1b[90m", "")
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// drawDistribution：单行堆叠占比条 + 可点击图例（所有项目画在一条直线上）
func (m *model) drawDistribution(c *canvas, p float64) {
	m.section(c, "三 · 占比分布（单行堆叠图）")
	lp := easeOutCubic(m.distP)

	// 页签（可点击）
	c.add("  ", "", nil)
	c.addStyled("图表:  ", "\x1b[90m", "")
	for ti, name := range []string{"模块", "语言"} {
		id := fmt.Sprintf("tab:%d", ti)
		label := " " + name + " "
		var style string
		switch {
		case m.tab == ti:
			style = "\x1b[1m\x1b[97m\x1b[46m"
		case m.hover == id:
			style = "\x1b[4m\x1b[37m"
		default:
			style = "\x1b[90m"
		}
		c.addStyled(label, style, id)
		c.add("  ", "", nil)
	}
	c.newline()
	c.newline()

	gs := m.groups()
	if len(gs) == 0 {
		return
	}
	total := 0
	for _, g := range gs {
		total += g.Code
	}
	if total == 0 {
		c.add("  暂无数据", "", nil)
		c.newline()
		return
	}

	// 单行堆叠条：累计取整保证无缝铺满轨道
	W := c.maxW - 4
	cum, prev := 0, 0
	for _, g := range gs {
		cum += g.Code
		end := int(math.Round(float64(W) * float64(cum) / float64(total) * lp))
		wSeg := end - prev
		prev = end
		if wSeg <= 0 {
			continue
		}
		bg := bgSeq(g.Color)
		if m.hover != "grp:"+g.Name && m.selected != g.Name {
			bg = bgSeq(dimColor(g.Color))
		}
		segID := "grp:" + g.Name
		c.add(strings.Repeat(" ", wSeg), segID, func(string) string { return bg + strings.Repeat(" ", wSeg) + reset })
	}
	if rem := W - prev; rem > 0 {
		track := bgSeq("#232733")
		c.add(strings.Repeat(" ", rem), "", func(string) string { return track + strings.Repeat(" ", rem) + reset })
	}
	c.newline()
	c.newline()

	// 图例（可点击、自动换行、百分比跟随动画增长）
	for _, g := range gs {
		pct := float64(g.Code) / float64(total) * 100 * lp
		itemID := "grp:" + g.Name
		var nameStyle string
		switch {
		case m.selected == g.Name:
			nameStyle = "\x1b[1m\x1b[4m" + fgSeq(g.Color)
		case m.hover == itemID:
			nameStyle = "\x1b[4m" + fgSeq(g.Color)
		default:
			nameStyle = fgSeq(g.Color)
		}
		name := g.Name
		pctText := fmt.Sprintf("%.1f%%", pct)
		itemW := 2 + 1 + runewidth.StringWidth(name) + 1 + len(pctText) + 3
		if c.curW+itemW > c.maxW-2 {
			c.newline()
			c.add("  ", "", nil)
		}
		chip := bgSeq(g.Color)
		c.add("  ", itemID, func(string) string { return chip + "  " + reset })
		c.add(" ", "", nil)
		c.addStyled(name, nameStyle, itemID)
		c.addStyled(" "+pctText, "\x1b[90m", itemID)
		c.add("   ", "", nil)
	}
	c.newline()
	c.newline()

	m.drawDetail(c)
}

type panelRow struct{ k, v string }

type linkRow struct {
	text, id, tail string
}

// drawDetail：选中组或选中文件的手绘圆角面板
func (m *model) drawDetail(c *canvas) {
	if m.fileRel != "" {
		if fd, ok := m.stats.FileMap[m.fileRel]; ok {
			share := pctOf(fd.Code, m.stats.Totals.Code)
		rows := []panelRow{
			{"路径", fd.Rel},
			{"所属模块", orDash(fd.Group)},
			{"代码行", num(fd.Code) + " 行"},
			{"注释行", fmt.Sprintf("%s 行 (%.1f%%)", num(fd.Comment), pctOf(fd.Comment, fd.Total))},
			{"空行", fmt.Sprintf("%s 行 (%.1f%%)", num(fd.Blank), pctOf(fd.Blank, fd.Total))},
			{"总行数", fmt.Sprintf("%s 行  ·  占全项目代码 %.2f%%", num(fd.Total), share)},
			{"文件大小", SizeStr(fd.Size)},
		}
			m.panel(c, fd.Color, "文件详情", rows, nil)
			return
		}
	}
	g := m.groupByName(m.selected)
	if g == nil {
		return
	}
	share := pctOf(g.Code, m.stats.Totals.Code)
	rows := []panelRow{
		{"文件数", fmt.Sprintf("%d 个", g.Files)},
		{"代码行", num(g.Code) + " 行"},
		{"注释行", fmt.Sprintf("%s 行 (%.1f%%)", num(g.Comment), pctOf(g.Comment, g.Total))},
		{"空行", fmt.Sprintf("%s 行 (%.1f%%)", num(g.Blank), pctOf(g.Blank, g.Total))},
		{"总行数", fmt.Sprintf("%s 行  ·  占全项目 %.1f%%", num(g.Total), share)},
		{"文件体积", fmt.Sprintf("%s  ·  占代码体积 %.1f%%", SizeStr(g.Size), pctSize(g.Size, m.stats.Totals.Size))},
	}
	var links []linkRow
	for i, fs := range m.stats.GroupTop[g.Name] {
		links = append(links, linkRow{
			fmt.Sprintf("%d. %s", i+1, fs.Rel),
			"file:" + fs.Rel,
			num(fs.Code) + " 行",
		})
	}
	m.panel(c, g.Color, g.Name+" · 明细", rows, links)
}

// panel 手绘圆角面板，宽 PW，内容可登记命中区域
func (m *model) panel(c *canvas, color, title string, rows []panelRow, links []linkRow) {
	PW := c.maxW - 2
	bc := fgSeq(color)
	c.newline()
	c.add("  ", "", nil)
	c.addStyled("╭─ ", bc, "")
	c.addStyled(title, "\x1b[1m"+bc, "")
	c.addStyled(" " + strings.Repeat("─", max(0, PW-5-runewidth.StringWidth(title))) + "╮", bc, "")
	c.newline()

	for _, row := range rows {
		v := cut(row.v, PW-20)
		c.add("  ", "", nil)
		c.addStyled("│  ", bc, "")
		c.addStyled(pad(row.k, 12), "\x1b[90m", "")
		c.addStyled(v, "\x1b[97m", "")
		c.add(strings.Repeat(" ", max(0, PW-16-runewidth.StringWidth(v))), "", nil)
		c.addStyled("│", bc, "")
		c.newline()
	}
	for _, lk := range links {
		t := cut(lk.text, PW-30)
		tail := cut(lk.tail, 12)
		c.add("  ", "", nil)
		c.addStyled("│  ", bc, "")
		st := "\x1b[96m"
		if m.hover == lk.id {
			st = "\x1b[4m\x1b[1m\x1b[96m"
		}
		c.addStyled(t, st, lk.id)
		gap := max(1, PW-5-runewidth.StringWidth(t)-runewidth.StringWidth(tail))
		c.add(strings.Repeat(" ", gap), "", nil)
		c.addStyled(tail, "\x1b[90m", lk.id)
		c.add(" ", "", nil)
		c.addStyled("│", bc, "")
		c.newline()
	}

	c.add("  ", "", nil)
	c.addStyled("╰"+strings.Repeat("─", PW-2)+"╯", bc, "")
	c.newline()
}

func (m *model) drawTopFiles(c *canvas, p float64) {
	if len(m.stats.Top) == 0 {
		return
	}
	m.section(c, "四 · 最大代码文件 Top 10")
	lp := sub(p, 0.52, 0.3)
	maxCode := m.stats.Top[0].Code
	for i, fs := range m.stats.Top {
		col := "#56B6C2"
		size := int64(0)
		if fd, ok := m.stats.FileMap[fs.Rel]; ok {
			col = fd.Color
			size = fd.Size
		}
		fill := clampInt(int(math.Round(10*float64(fs.Code)/math.Max(1, float64(maxCode))*lp)), 0, 10)
		id := "file:" + fs.Rel
		isSel := m.fileRel == fs.Rel
		isHover := m.hover == id
		c.newline()
		c.add("  ", "", nil)
		marker := "  "
		if isSel {
			marker = "▶ "
		}
		c.addStyled(marker+padL2(strconv.Itoa(i+1), 2)+".", "\x1b[93m", id)
		c.add("  ", "", nil)
		c.addStyled(padL2(num(fs.Code), 7), "\x1b[96m", id)
		c.add("  ", "", nil)
		st := fgSeq(col)
		if isSel {
			st = "\x1b[1m" + fgSeq(col)
		} else if isHover {
			st = "\x1b[4m" + fgSeq(col)
		}
		c.addStyled(strings.Repeat("█", fill), st, id)
		c.addStyled(strings.Repeat("░", max(0, 10-fill)), "\x1b[90m", id)
		c.add("  ", "", nil)
		c.addStyled(padL2(SizeStr(size), 8), "\x1b[90m", id)
		c.add("  ", "", nil)
		pathSt := "\x1b[37m"
		if isSel {
			pathSt = "\x1b[1m\x1b[97m"
		} else if isHover {
			pathSt = "\x1b[4m\x1b[97m"
		}
		c.addStyled(cut(fs.Rel, c.maxW-40), pathSt, id)
	}
}

func (m *model) drawMetrics(c *canvas) {
	s := m.stats
	m.section(c, "五 · 质量与规模指标")
	avg := 0
	if s.Totals.Files > 0 {
		avg = s.Totals.Code / s.Totals.Files
	}
	avgSize := int64(0)
	if s.Totals.Files > 0 {
		avgSize = s.Totals.Size / int64(s.Totals.Files)
	}
	rows := [][4]string{
		{"计入统计文件", fmt.Sprintf("%d 个", s.Totals.Files), "纯代码行", num(s.Totals.Code) + " 行"},
		{"注释行", fmt.Sprintf("%s 行 (%.1f%%)", num(s.Totals.Comment), pctOf(s.Totals.Comment, s.Totals.Total)), "空行", fmt.Sprintf("%s 行 (%.1f%%)", num(s.Totals.Blank), pctOf(s.Totals.Blank, s.Totals.Total))},
		{"总行数", fmt.Sprintf("%s 行（代码+注释+空行）", num(s.Totals.Total)), "平均文件长度", fmt.Sprintf("%d 行", avg)},
		{"注释/代码比", ratio(s.Totals.Comment, s.Totals.Code), "代码总体积", SizeStr(s.Totals.Size)},
		{"平均文件体积", SizeStr(avgSize), "最大单文件体积", SizeStr(s.MaxFileSize)},
		{"扫描目录", fmt.Sprintf("%d 个", s.DirsFound), "统计耗时", fmt.Sprintf("%.2f 秒", s.Elapsed.Seconds())},
	}
	for _, r := range rows {
		c.newline()
		c.add("  ", "", nil)
		c.addStyled(pad(r[0], 16), "\x1b[90m", "")
		c.addStyled(pad(r[1], 30), "\x1b[97m", "")
		c.addStyled(pad(r[2], 16), "\x1b[90m", "")
		c.addStyled(r[3], "\x1b[97m", "")
	}
}

func ratio(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", float64(a)/float64(b))
}
