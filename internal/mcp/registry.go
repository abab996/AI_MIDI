package mcp

import (
	"aimidi/internal/llm"
)

var mcpTools = []llm.ToolDefinition{
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "read_library_file",
			Description: "读取 Library 目录下的乐理知识文件内容。在创作音乐或回答专业乐理问题前必须先调用此工具。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "文件名（如 02_配和弦指南.md）",
					},
				},
				"required": []string{"filename"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "list_midi_files",
			Description: "列出当前项目所有 MIDI 文件及其大小（含子目录，路径为相对项目目录）。",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "parse_midi",
			Description: "解析 output 或项目目录下的 MIDI 文件，返回 note_table 格式的音符数据。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "MIDI 文件名（如 output.mid、song.mid）",
					},
				},
				"required": []string{"filename"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "create_midi",
			Description: "从 note_table 数据创建 MIDI 文件，保存到 output 或项目目录。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "输出文件名（如 melody.mid），默认 output.mid",
					},
					"bpm": map[string]any{
						"type":        "integer",
						"description": "速度（如 120），默认 120",
					},
					"notes": map[string]any{
						"type":        "string",
						"description": "note_table 格式的音符数据，每行一个音符",
					},
					"note_table": map[string]any{
						"type":        "string",
						"description": "同 notes，兼容参数名",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "delete_midi",
			Description: "删除 output 或项目目录下的 MIDI 文件。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "要删除的 MIDI 文件名（如 old.mid）",
					},
				},
				"required": []string{"filename"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "create_folder",
			Description: "在项目目录下创建文件夹（支持多级子目录）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "文件夹相对路径（如 drums、sectionA/drums）",
					},
				},
				"required": []string{"name"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "list_project_structure",
			Description: "以树状图列出当前项目所有 MIDI 文件的目录层级结构。",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
}

// AskUserTool 本地交互提问工具定义
var AskUserTool = llm.ToolDefinition{
	Type: "function",
	Function: llm.FunctionSchema{
		Name:        "ask_user_question",
		Description: "当用户需求不明确、存在多个合理创作方向、或关键参数（风格/BPM/调式/结构等）缺失时，向用户提出结构化问题以确认意图。不要在你已有足够信息时使用；一次最多提出 4 个问题，每题提供 2~5 个选项，如需多个答案可设 multiSelect=true。用户回答后按回答继续创作。",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"questions"},
			"properties": map[string]any{
				"questions": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 4,
					"items": map[string]any{
						"type":     "object",
						"required": []string{"question", "header", "options"},
						"properties": map[string]any{
							"question": map[string]any{
								"type":        "string",
								"description": "完整问题文本，以问号结尾",
							},
							"header": map[string]any{
								"type":        "string",
								"description": "短标签（≤12 字符），如 '风格'",
							},
							"options": map[string]any{
								"type":     "array",
								"minItems": 2,
								"maxItems": 5,
								"items": map[string]any{
									"type":     "object",
									"required": []string{"label"},
									"properties": map[string]any{
										"label": map[string]any{
											"type":        "string",
											"description": "选项显示文本（1~5 个词）",
										},
										"description": map[string]any{
											"type":        "string",
											"description": "选该项的后果/含义说明，可省略",
										},
									},
								},
							},
							"multiSelect": map[string]any{
								"type":        "boolean",
								"description": "是否允许多选，默认 false",
							},
						},
					},
				},
			},
		},
	},
}

// GetMCPTools 返回当前可用的 MCP 工具列表
func GetMCPTools(projectID string) []llm.ToolDefinition {
	return mcpTools
}

// GetLocalTools 返回本地交互工具列表
func GetLocalTools() []llm.ToolDefinition {
	return []llm.ToolDefinition{AskUserTool}
}
