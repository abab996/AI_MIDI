package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ArrangementMaxBytes 编排数据写入上限（5MB）
const ArrangementMaxBytes = 5 << 20

// ArrangementFile 返回项目编排数据文件路径
func ArrangementFile(projectID string) (string, error) {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(pdir, "arrangement.json"), nil
}

// ReadArrangement 读取编排数据；文件不存在时返回 (nil, nil)
func ReadArrangement(projectID string) (json.RawMessage, error) {
	file, err := ArrangementFile(projectID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取编排数据失败: %w", err)
	}
	return json.RawMessage(data), nil
}

// WriteArrangement 校验并原子写入编排数据
func WriteArrangement(projectID string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("编排数据不能为空")
	}
	if len(data) > ArrangementMaxBytes {
		return fmt.Errorf("编排数据过大 (%d bytes，上限 %d bytes)", len(data), ArrangementMaxBytes)
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("非法的 JSON 数据")
	}

	file, err := ArrangementFile(projectID)
	if err != nil {
		return err
	}
	pretty := data
	if ind, err := json.MarshalIndent(v, "", "  "); err == nil {
		pretty = ind
	}

	tmp := file + fmt.Sprintf(".tmp.%d.%d", os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, pretty, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换编排数据失败: %w", err)
	}
	return nil
}
