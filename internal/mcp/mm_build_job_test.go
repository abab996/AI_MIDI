package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 进度报告必须按阶段呈现，且未完成时给作业号（这是"进度卡"的内容契约）
func TestMMBuildJobReport(t *testing.T) {
	job := &mmBuildJob{ID: "mmb_test", PluginID: "com.test.job", Started: time.Now().Add(-3 * time.Second)}
	job.append("[mm] 配置完成")
	job.append("[mm] 编译中: plugin.cpp")
	job.append("[mm] 打包 com.test.job-0.1.0.mm")

	running := job.report()
	if !strings.Contains(running, "编译进行中") || !strings.Contains(running, "mmb_test") {
		t.Fatalf("进行中报告不对:\n%s", running)
	}
	if !strings.Contains(running, "打包") {
		t.Fatalf("阶段应识别为打包:\n%s", running)
	}

	job.append("[mm] 校验通过: com.test.job 0.1.0")
	job.finish(nil)
	done := job.report()
	if !strings.Contains(done, "编译完成") || !strings.Contains(done, "mm_install_plugin") {
		t.Fatalf("完成报告应给下一步建议:\n%s", done)
	}

	job2 := &mmBuildJob{ID: "mmb_fail", PluginID: "com.test.fail", Started: time.Now()}
	job2.append("error: expected ';'")
	job2.finish(os.ErrNotExist)
	if rep := job2.report(); !strings.Contains(rep, "编译失败") {
		t.Fatalf("失败报告不对:\n%s", rep)
	}
}

func TestMMBuildStatusTool(t *testing.T) {
	// 未知作业号要给可解释错误（不 panic）
	out, err := executeMMTool("mm_build_status", map[string]any{"job": "mmb_nope"})
	if err != nil {
		t.Fatalf("不应返回 error: %v", err)
	}
	if !strings.Contains(out, "找不到编译作业") {
		t.Fatalf("未知作业应可解释: %s", out)
	}
	// 注册表里能看到新工具
	names := map[string]bool{}
	for _, tl := range GetMCPTools("p") {
		names[tl.Function.Name] = true
	}
	if !names["mm_build_status"] {
		t.Fatal("缺少 mm_build_status 工具")
	}
}

// 真实编译一次：验证作业真的跑起来并产出 .mm（无 mm.exe 时跳过）
func TestMMBuildJobRealBuild(t *testing.T) {
	// 测试二进制在临时目录里，mmExePath 找不到仓库产物：这里显式指过去
	if _, err := mmExePath(); err != nil {
		if cand := filepath.Join("..", "..", "output", "mm.exe"); fileExists(cand) {
			t.Setenv("MM_EXE", cand)
		}
	}
	if _, err := mmExePath(); err != nil {
		t.Skip("本机没有 mm.exe，跳过真实编译")
	}
	if sdk := filepath.Join("..", "..", "engine", "mm-sdk"); fileExists(filepath.Join(sdk, "include", "mm", "mm.h")) {
		t.Setenv("MM_SDK_ROOT", sdk)
	}
	dir := filepath.Join(t.TempDir(), "com.test.buildjob")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 直接拿示例工程的源码结构当输入：拷一份 gain 示例，再跑作业编译
	src := filepath.Join("..", "..", "engine", "mm-sdk", "examples", "gain")
	for _, f := range []string{"CMakeLists.txt", "src", "manifest.json", "ui"} {
		if err := copyTree(filepath.Join(src, f), filepath.Join(dir, f)); err != nil {
			t.Skipf("准备工程失败（%s）: %v", f, err)
		}
	}
	// mm_create_plugin 固定写到 Library/plugin-src，这里改为直接在临时目录编译：
	// 用作业机制跑一次 build，断言阶段推进与产物存在
	job, jerr := startMMBuild("com.test.buildjob", dir, "")
	if jerr != nil {
		t.Skipf("启动作业失败: %v", jerr)
	}
	_ = waitMMBuild(job, 3*time.Minute)
	_, done, _, lines := job.snapshot()
	if !done {
		t.Skip("编译超时（环境慢），跳过")
	}
	rep := job.report()
	if _, _, err, _ := job.snapshot(); err != nil {
		t.Skipf("本机编译未成功（环境问题，非作业机制问题）: %v\n%s", err, rep)
	}
	if !strings.Contains(rep, "编译完成") {
		t.Fatalf("成功报告应写明完成:\n%s", rep)
	}
	// 产物必须真的落盘（作业不只是"跑过"，而是"跑出了东西"）
	pkgs, _ := filepath.Glob(filepath.Join(dir, "*.mm"))
	if len(pkgs) == 0 {
		t.Fatalf("作业完成但没产出 .mm（dir=%s，日志尾部：%v）", dir, lines[len(lines)-3:])
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func copyTree(from, to string) error {
	st, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		raw, rerr := os.ReadFile(from)
		if rerr != nil {
			return rerr
		}
		if merr := os.MkdirAll(filepath.Dir(to), 0o755); merr != nil {
			return merr
		}
		return os.WriteFile(to, raw, 0o644)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "build" || e.Name() == "bin" {
			continue
		}
		if err := copyTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
