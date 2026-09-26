package app

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"aimidi/internal/config"
)

// 窗口状态记忆：尺寸/位置/最大化随退出保存、随启动恢复。
// 此前每次启动都按屏幕比例 0.8 重新居中弹出且不可缩小到 0.64×工作区
// 以下——分屏/小屏用户无法保留自己调好的窗口布局。
// 独立 window-state.json 而非塞进 settings.json：窗口几何是设备本地
// 偏好，与"配置迁移/导出"语义无关（对齐 theme.txt 的存放方式）。

const windowStateFile = "window-state.json"

// WindowState 逻辑像素单位（与 Wails runtime.Get/Set* 语义一致）
type WindowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Maximized bool `json:"maximized"`
}

func windowStatePath() string {
	return filepath.Join(config.ProjectRoot, windowStateFile)
}

// LoadWindowState 读取上次保存的窗口状态；不存在/损坏/字段非法时 ok=false
func LoadWindowState() (WindowState, bool) {
	var st WindowState
	data, err := os.ReadFile(windowStatePath())
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false
	}
	// 宽高至少要有可用值；x/y 允许负数（多显示器负坐标区）
	if st.Width < 200 || st.Height < 150 {
		return st, false
	}
	return st, true
}

// SaveWindowState 原子落盘窗口状态；失败仅记日志（不值得打断退出流程）
func SaveWindowState(st WindowState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := windowStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		slog.Debug("窗口状态写入失败", "err", err)
		return
	}
	if err := os.Rename(tmp, windowStatePath()); err != nil {
		slog.Debug("窗口状态替换失败", "err", err)
	}
}
