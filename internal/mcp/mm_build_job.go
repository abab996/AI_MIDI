package mcp

// MM 插件编译的**可观测长任务**（P6 §4.8 一键编译进度卡）
//
// 为什么需要：`mm build` 是配置 + 编译 + 收集 + 打包 + 校验的长过程（首次编译动辄
// 几十秒到几分钟）。工具若只同步等待，对话里就是一段"卡住没有任何反馈"的空白；
// 直接返回又会截断日志。这里把编译变成可轮询的后台作业：
//   · mm_build_plugin 启动作业并等一小段时间（快编译直接拿到结果）；
//   · 未完成则返回"进度报告 + job id"，AI/界面可用 mm_build_status 继续查；
//   · 报告本身按阶段呈现（配置 → 编译 → 收集 → 打包 → 校验），
//     这就是"进度卡"的内容——对话界面本就会渲染工具结果，无需另造组件。

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// mmBuildPhase 阶段门（按 mm build 的实际输出识别，识别不到就归为"编译中"）
type mmBuildPhase int

const (
	phaseConfig mmBuildPhase = iota
	phaseCompile
	phaseCollect
	phasePack
	phaseValidate
	phaseDone
)

func (p mmBuildPhase) label() string {
	switch p {
	case phaseConfig:
		return "配置"
	case phaseCompile:
		return "编译"
	case phaseCollect:
		return "收集"
	case phasePack:
		return "打包"
	case phaseValidate:
		return "校验"
	default:
		return "完成"
	}
}

func mmBuildPhaseOfLine(line string) (mmBuildPhase, bool) {
	switch {
	case strings.Contains(line, "配置") || strings.Contains(line, "cmake"):
		return phaseConfig, true
	case strings.Contains(line, "编译") || strings.Contains(line, "Compiling") || strings.Contains(line, "[mm] 构建"):
		return phaseCompile, true
	case strings.Contains(line, "收集") || strings.Contains(line, "复制") || strings.Contains(line, "拷贝"):
		return phaseCollect, true
	case strings.Contains(line, "打包") || strings.Contains(line, "已生成"):
		return phasePack, true
	case strings.Contains(line, "校验通过") || strings.Contains(line, "校验失败") || strings.Contains(line, "MM0"):
		return phaseValidate, true
	}
	return phaseConfig, false
}

// mmBuildJob 一次编译作业（并发安全；日志只留尾部，避免长任务吃内存）
type mmBuildJob struct {
	ID       string
	PluginID string
	Dir      string
	Started  time.Time

	mu    sync.Mutex
	lines []string
	phase mmBuildPhase
	done  bool
	err   error
}

const mmBuildLogKeep = 200 // 保留的最大行数

// startMMBuild 启动后台编译（返回作业）。
func startMMBuild(pluginID, dir, sdk string) (*mmBuildJob, error) {
	exe, err := mmExePath()
	if err != nil {
		return nil, err
	}
	job := &mmBuildJob{
		ID:       fmt.Sprintf("mmb_%d", time.Now().UnixNano()),
		PluginID: pluginID,
		Dir:      dir,
		Started:  time.Now(),
	}
	mmBuildJobs.Store(job.ID, job)

	args := []string{"build", dir}
	if sdk != "" {
		args = append(args, "--sdk", sdk)
	}
	cmd := exec.Command(exe, args...)
	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		mmBuildJobs.Delete(job.ID)
		return nil, perr
	}
	cmd.Stderr = cmd.Stdout
	if serr := cmd.Start(); serr != nil {
		mmBuildJobs.Delete(job.ID)
		return nil, serr
	}
	go func() {
		buf := make([]byte, 4096)
		var pending string
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				pending += string(buf[:n])
				for {
					i := strings.IndexAny(pending, "\r\n")
					if i < 0 {
						break
					}
					line := strings.TrimSpace(pending[:i])
					pending = pending[i+1:]
					if line == "" {
						continue
					}
					job.append(line)
				}
			}
			if rerr != nil {
				break
			}
		}
		if s := strings.TrimSpace(pending); s != "" {
			job.append(s)
		}
		job.finish(cmd.Wait())
	}()
	return job, nil
}

func (j *mmBuildJob) append(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, line)
	if len(j.lines) > mmBuildLogKeep {
		j.lines = j.lines[len(j.lines)-mmBuildLogKeep:]
	}
	if p, ok := mmBuildPhaseOfLine(line); ok && p >= j.phase {
		j.phase = p
	}
}

func (j *mmBuildJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done = true
	j.err = err
	if err == nil {
		j.phase = phaseDone
	}
}

// snapshot 取当前状态（供报告与测试）
func (j *mmBuildJob) snapshot() (phase mmBuildPhase, done bool, err error, lines []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.phase, j.done, j.err, append([]string(nil), j.lines...)
}

// report 进度卡的可读内容：阶段 + 耗时 + 尾部日志 + 下一步
func (j *mmBuildJob) report() string {
	phase, done, err, lines := j.snapshot()
	elapsed := time.Since(j.Started).Round(time.Second)
	var b strings.Builder
	if !done {
		fmt.Fprintf(&b, "⏳ 编译进行中：%s · 阶段 %s · 已用时 %s\n", j.PluginID, phase.label(), elapsed)
		fmt.Fprintf(&b, "作业号 %s —— 可用 mm_build_status 继续查看进度。\n", j.ID)
	} else if err != nil {
		fmt.Fprintf(&b, "✗ 编译失败：%s · 阶段 %s · 用时 %s\n", j.PluginID, phase.label(), elapsed)
	} else {
		fmt.Fprintf(&b, "✓ 编译完成：%s · 用时 %s\n", j.PluginID, elapsed)
	}
	if len(lines) > 0 {
		tail := lines
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
			b.WriteString("…（前段省略）\n")
		}
		b.WriteString(strings.Join(tail, "\n"))
		b.WriteString("\n")
	}
	if done && err == nil {
		b.WriteString("下一步：mm_install_plugin（需要时先与用户确认能力清单）")
	}
	return b.String()
}

var mmBuildJobs sync.Map // id -> *mmBuildJob

func mmBuildJobByID(id string) (*mmBuildJob, bool) {
	v, ok := mmBuildJobs.Load(strings.TrimSpace(id))
	if !ok {
		return nil, false
	}
	j, ok := v.(*mmBuildJob)
	return j, ok
}

// waitMMBuild 等作业完成最多 timeout；返回是否已完成
func waitMMBuild(job *mmBuildJob, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, done, _, _ := job.snapshot(); done {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
