package project

import (
	"os"
	"path/filepath"

	"melodymate/internal/config"
)

// SaveDraft 保存项目输入框草稿
func SaveDraft(projectID, text string) error {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(pdir, 0755)
	draftFile := filepath.Join(pdir, config.DraftFilename)
	return os.WriteFile(draftFile, []byte(text), 0644)
}

// LoadDraft 读取项目输入框草稿
func LoadDraft(projectID string) string {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return ""
	}
	draftFile := filepath.Join(pdir, config.DraftFilename)
	data, err := os.ReadFile(draftFile)
	if err != nil {
		return ""
	}
	return string(data)
}
