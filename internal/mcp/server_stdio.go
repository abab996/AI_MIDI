package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// RunMCPServer 以 stdio 传输运行 MCP JSON-RPC 服务
func RunMCPServer() {
	envOutput := os.Getenv("AI_MIDI_OUTPUT_DIR")
	if envOutput == "" {
		envOutput = config.OutputDir
	}
	envMirror := os.Getenv("AI_MIDI_MIRROR_DIR")

	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

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

			resText, _ := ExecuteTool(callParams.Name, callParams.Arguments, filepath.Clean(envOutput), filepath.Clean(envMirror))
			sendResponse(req.ID, map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": resText,
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
