package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aimidi/internal/config"
)

// ListLibraryFiles 返回 Library 目录下所有 .md 文件名列表
func ListLibraryFiles() []string {
	if _, err := os.Stat(config.LibraryDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(config.LibraryDir)
	if err != nil {
		return nil
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}

// ReadLibraryFile 读取指定乐理文件内容，做路径逃逸防御
func ReadLibraryFile(filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || strings.Contains(filename, "\x00") || strings.Contains(filename, "..") {
		return "", fmt.Errorf("非法的文件名: %s", filename)
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
		return "", fmt.Errorf("文件不存在 — %s\n可用文件：%s", filename, strings.Join(available, ", "))
	}

	return string(data), nil
}
