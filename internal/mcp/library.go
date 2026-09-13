package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aimidi/internal/config"
)

// IsKnowledgeFile 判断文件名是否为知识库文本文件（.md/.txt，大小写不敏感）。
// 仅放行纯文本两种格式：Library 下还存放 soundfonts 等二进制资源，
// 扩展名白名单防止 read_library_file 把二进制读进模型上下文
func IsKnowledgeFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt")
}

// ListLibraryFiles 返回 Library 目录下的知识文件名列表：内置文件取根目录
// 的 .md/.txt；用户自定义文件（Library/user/，设置页「知识库」分区可管理）
// 以 config.LibraryUserPrefix 前缀返回。内置在前、用户文件在后，各自排序。
func ListLibraryFiles() []string {
	if _, err := os.Stat(config.LibraryDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(config.LibraryDir)
	if err != nil {
		return nil
	}

	var builtin, user []string
	for _, e := range entries {
		if !e.IsDir() && IsKnowledgeFile(e.Name()) {
			builtin = append(builtin, e.Name())
		}
	}
	if userEntries, err := os.ReadDir(config.LibraryUserDir); err == nil {
		for _, e := range userEntries {
			if !e.IsDir() && IsKnowledgeFile(e.Name()) {
				user = append(user, config.LibraryUserPrefix+e.Name())
			}
		}
	}
	sort.Strings(builtin)
	sort.Strings(user)
	return append(builtin, user...)
}

// ReadLibraryFile 读取指定乐理文件内容，做路径逃逸防御。
// filename 可含 user/ 前缀（用户自定义文件，即 Library/user/ 下的相对路径）
func ReadLibraryFile(filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || strings.Contains(filename, "\x00") || strings.Contains(filename, "..") {
		return "", fmt.Errorf("非法的文件名: %s", filename)
	}
	if !IsKnowledgeFile(filename) {
		return "", fmt.Errorf("仅支持读取 .md/.txt 知识文件: %s", filename)
	}

	target := filepath.Join(config.LibraryDir, filename)
	targetClean := filepath.Clean(target)
	libClean := filepath.Clean(config.LibraryDir)

	rel, err := filepath.Rel(libClean, targetClean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("路径逃逸检测: %s", filename)
	}

	data, err := os.ReadFile(targetClean)
	if err != nil {
		available := ListLibraryFiles()
		if len(available) == 0 {
			// 知识库目录整体缺失/为空（典型：安装包未带 Library，此前
			// AI_MIDI.iss 漏装）：给出可行动的提示而不是裸的
			// 「文件不存在 + 空列表」，让 AI 能向用户说明真实原因
			return "", fmt.Errorf("知识库不可用（目录缺失或为空）：%s。程序可能未完整安装，请提醒用户重新运行安装程序；本次请基于自身知识回答，不要重试读取", libClean)
		}
		return "", fmt.Errorf("文件不存在 — %s\n可用文件：%s", filename, strings.Join(available, ", "))
	}

	return string(data), nil
}
