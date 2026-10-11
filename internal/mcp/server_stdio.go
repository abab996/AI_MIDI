package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aimidi/internal/config"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// toolErrorPrefix 是 ExecuteTool 对失败结果使用的统一前缀。
// 它把错误包成文本并以 nil error 返回（见 executor.go 各分支），
// 因此这里据此判定工具调用是否失败，从而按 MCP 规范设置 isError。
//
// 注意有两个前缀：多数失败用「错误：」，未知工具走的是「未知工具：」
// （executor.go 的 default 分支），漏掉后者会让未知工具被当成成功。
const (
	toolErrorPrefix   = "错误："
	toolUnknownPrefix = "未知工具："
)

// resourceURIPrefix 知识库资源的 URI 前缀。
// 资源 URI 形如 music://library/02_配和弦指南.md，
// 用户自定义文件带 user/ 前缀：music://library/user/my-notes.md
const resourceURIPrefix = "music://library/"

// isToolFailure 判断 ExecuteTool 返回的文本是否代表一次失败的调用。
// 前缀匹配是当前实现的唯一可行判据：错误被包成文本、error 恒为 nil。
// 若将来 executor 改为返回真实 error，这里应优先用 error 判断。
func isToolFailure(text string) bool {
	return strings.HasPrefix(text, toolErrorPrefix) || strings.HasPrefix(text, toolUnknownPrefix)
}

// RunMCPServer 以 stdio 传输运行 MCP JSON-RPC 服务
func RunMCPServer() {
	RunMCPServerIO(os.Stdin, os.Stdout)
}

// RunMCPServerIO 是 RunMCPServer 的可测试内核：从 r 读请求、往 w 写响应。
// 拆出这一层是为了让协议行为（能力声明、resources/*、isError）能被单元测试
// 覆盖——直接替换 os.Stdin/os.Stdout 在 Windows 上用管道会死锁。
func RunMCPServerIO(r io.Reader, w io.Writer) {
	envOutput := os.Getenv("AI_MIDI_OUTPUT_DIR")
	if envOutput == "" {
		envOutput = config.OutputDir
	}
	envMirror := os.Getenv("AI_MIDI_MIRROR_DIR")

	reader := bufio.NewReader(r)
	writer := w

	sendResponse := func(id any, result any, errObj any) {
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result:  result,
			Error:   errObj,
		}
		data, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(writer, "%s\n", string(data))
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			sendResponse(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
					// 知识库以资源形式暴露：模型按需读取，不必整篇进上下文
					"resources": map[string]any{
						"subscribe":   false,
						"listChanged": false,
					},
				},
				"serverInfo": map[string]any{
					"name":    "ai-midi-tools",
					"version": "1.0.0",
				},
			}, nil)

		case "tools/list":
			var toolsOut []map[string]any
			for _, t := range mcpTools {
				toolsOut = append(toolsOut, map[string]any{
					"name":        t.Function.Name,
					"description": t.Function.Description,
					"inputSchema": t.Function.Parameters,
				})
			}
			sendResponse(req.ID, map[string]any{"tools": toolsOut}, nil)

		case "tools/call":
			var callParams struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &callParams)

			resText, execErr := ExecuteTool(callParams.Name, callParams.Arguments, filepath.Clean(envOutput), filepath.Clean(envMirror))
			result := map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": resText,
					},
				},
			}
			// 执行失败必须显式标记，否则调用方会把错误文本当成正常结果继续处理。
			if execErr != nil || isToolFailure(resText) {
				result["isError"] = true
			}
			sendResponse(req.ID, result, nil)

		case "resources/list":
			names := ListLibraryFiles()
			resources := make([]map[string]any, 0, len(names))
			for _, name := range names {
				resources = append(resources, map[string]any{
					"uri":         resourceURIPrefix + name,
					"name":        name,
					"title":       name,
					"description": "AI_MIDI 乐理知识库文件",
					"mimeType":    "text/markdown",
				})
			}
			sendResponse(req.ID, map[string]any{"resources": resources}, nil)

		case "resources/read":
			var readParams struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(req.Params, &readParams)

			// 只接受本服务声明的 URI 前缀；其余一律按文件名处理（便于直接指定
			// Library 下的相对路径，如 user/my-notes.md）。
			name := strings.TrimSpace(strings.TrimPrefix(readParams.URI, resourceURIPrefix))
			if name == "" {
				sendResponse(req.ID, nil, map[string]any{
					"code":    -32602,
					"message": "uri 不能为空",
				})
				continue
			}
			content, readErr := ReadLibraryFile(name)
			if readErr != nil {
				sendResponse(req.ID, nil, map[string]any{
					"code":    -32602,
					"message": fmt.Sprintf("%v", readErr),
				})
				continue
			}
			sendResponse(req.ID, map[string]any{
				"contents": []map[string]any{
					{
						"uri":      resourceURIPrefix + name,
						"mimeType": "text/markdown",
						"text":     content,
					},
				},
			}, nil)

		default:
			sendResponse(req.ID, nil, map[string]any{
				"code":    -32601,
				"message": "Method not found",
			})
		}
	}
}
