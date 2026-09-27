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

// WriteArrangementVersioned 整份写入编排数据（前端保存路径）。
//
// 与 ApplyOps 共用项目级写锁，并维护单调递增的 rev：整份 PUT 与 AI 的定向
// 修改并发时，"后写覆盖先写"不可避免，但有了 rev，前端能判断自己手里这份
// 是不是最新（SSE 事件带回服务端 rev），从而丢弃过期的本地快照而不是覆盖。
//
// 内容以调用方为准：这里只补 rev，不改写结构（迁移由 ApplyOps/前端各自负责）。
func WriteArrangementVersioned(projectID string, data []byte) (int64, error) {
	unlock := LockArrangement(projectID)
	defer unlock()

	var arr map[string]any
	if err := json.Unmarshal(data, &arr); err != nil || arr == nil {
		return 0, fmt.Errorf("非法的 JSON 数据")
	}
	prev := int64(0)
	if raw, err := ReadArrangement(projectID); err == nil {
		if old := decodeArrangement(raw); old != nil {
			prev = intField(old, "rev")
		}
	}
	rev := prev + 1
	arr["rev"] = rev
	out, err := json.Marshal(arr)
	if err != nil {
		return 0, fmt.Errorf("序列化编排数据失败: %w", err)
	}
	if err := WriteArrangement(projectID, out); err != nil {
		return 0, err
	}
	return rev, nil
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
