package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// cleanupTestDir 删除测试在 output 目录下创建的临时文件夹，避免污染用户目录。
func cleanupTestDir(t *testing.T, name string) {
	t.Helper()
	target := filepath.Join(config.OutputDir, name)
	if err := os.RemoveAll(target); err != nil {
		t.Logf("清理测试目录失败（不致命）: %v", err)
	}
}

// runServer 用内存 reader/writer 驱动 RunMCPServerIO —— 无线程、无管道，
// 结果完全确定。（早期版本替换 os.Stdin/os.Stdout + os.Pipe，在 Windows 上会死锁。）
func runServer(t *testing.T, requests []map[string]any) []map[string]any {
	t.Helper()

	var in bytes.Buffer
	for _, req := range requests {
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		in.Write(data)
		in.WriteByte('\n')
	}

	var out bytes.Buffer
	RunMCPServerIO(&in, &out)

	var responses []map[string]any
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("响应不是合法 JSON: %q", line)
		}
		responses = append(responses, m)
	}
	return responses
}

func resultOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if e := resp["error"]; e != nil {
		t.Fatalf("期望 result，实际返回 error: %v", e)
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("响应缺少 result: %v", resp)
	}
	return result
}

// 资源能力必须被声明，否则客户端不会调用 resources/*（这是本 PR 的核心动机）。
func TestMCPServerAdvertisesResourcesCapability(t *testing.T) {
	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	result := resultOf(t, resp[0])

	caps, _ := result["capabilities"].(map[string]any)
	if caps == nil {
		t.Fatalf("initialize 未返回 capabilities")
	}
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities 缺少 tools")
	}
	if _, ok := caps["resources"]; !ok {
		t.Errorf("capabilities 缺少 resources —— 资源能力未声明")
	}
}

func TestMCPServerListsLibraryResources(t *testing.T) {
	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "resources/list", "params": map[string]any{}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	// resources/list 过去返回 -32601 Method not found，所以这里必须能拿到 result
	result := resultOf(t, resp[0])

	list, ok := result["resources"].([]any)
	if !ok {
		t.Fatalf("resources 字段不是数组: %v", result["resources"])
	}
	for _, item := range list {
		m, _ := item.(map[string]any)
		uri, _ := m["uri"].(string)
		if !strings.HasPrefix(uri, resourceURIPrefix) {
			t.Errorf("资源 uri 前缀不对: %q", uri)
		}
		if name, _ := m["name"].(string); name == "" {
			t.Errorf("资源缺少 name: %v", m)
		}
	}
}

func TestMCPServerReadsLibraryResource(t *testing.T) {
	files := ListLibraryFiles()
	if len(files) == 0 {
		t.Skip("知识库为空（Library 目录缺失），跳过读取测试")
	}
	first := files[0]

	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "resources/read",
			"params": map[string]any{"uri": resourceURIPrefix + first}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	result := resultOf(t, resp[0])

	contents, _ := result["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("期望 1 条 contents，得到 %d", len(contents))
	}
	c0, _ := contents[0].(map[string]any)
	if text, _ := c0["text"].(string); strings.TrimSpace(text) == "" {
		t.Errorf("读取到的内容为空: %s", first)
	}
	if c0["mimeType"] != "text/markdown" {
		t.Errorf("mimeType 应为 text/markdown，得到 %v", c0["mimeType"])
	}
}

func TestMCPServerReadUnknownResourceFails(t *testing.T) {
	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "resources/read",
			"params": map[string]any{"uri": resourceURIPrefix + "不存在的文件.md"}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	if resp[0]["error"] == nil {
		t.Errorf("读取不存在的资源应返回 error，实际: %v", resp[0])
	}
}

// 工具执行失败必须带 isError —— 否则调用方会把错误文本当成正常结果继续处理。
func TestMCPServerMarksToolErrors(t *testing.T) {
	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name":      "parse_midi",
				"arguments": map[string]any{"filename": "definitely-missing-file.mid"},
			}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	result := resultOf(t, resp[0])
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("工具执行失败时必须设置 isError=true，实际 result: %v", result)
	}
}

// 成功后不应带 isError（避免调用方误判）。
func TestMCPServerDoesNotMarkSuccessAsError(t *testing.T) {
	resp := runServer(t, []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name":      "create_folder",
				"arguments": map[string]any{"name": "test_iserror_dir"},
			}},
	})
	if len(resp) != 1 {
		t.Fatalf("期望 1 个响应，得到 %d", len(resp))
	}
	result := resultOf(t, resp[0])
	if isErr, _ := result["isError"].(bool); isErr {
		t.Errorf("成功调用不应设置 isError，实际 result: %v", result)
	}
	// 清理：把测试创建的目录删掉，别污染 output 目录
	if execText, _ := result["content"].([]any); len(execText) > 0 {
		if m, _ := execText[0].(map[string]any); m != nil {
			if txt, _ := m["text"].(string); strings.Contains(txt, "成功创建文件夹") {
				cleanupTestDir(t, "test_iserror_dir")
			}
		}
	}
}
