package project

import (
	"encoding/json"
	"fmt"
	"math"
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

// ArrangementConflict carries the current snapshot without changing the file.
type ArrangementConflict struct {
	Rev         int64
	Arrangement json.RawMessage
}

func (e *ArrangementConflict) Error() string {
	return "编排已被其他操作修改，请合并后保存"
}

// WriteArrangementVersioned compares the caller's revision under the same lock
// as ApplyOps. A lock alone cannot prevent a stale snapshot overwriting AI edits.
func WriteArrangementVersioned(projectID string, data []byte) (int64, error) {
	unlock := LockArrangement(projectID)
	defer unlock()

	var arr map[string]any
	if err := json.Unmarshal(data, &arr); err != nil || arr == nil {
		return 0, fmt.Errorf("非法的 JSON 数据")
	}
	base, ok := arr["rev"].(float64)
	if !ok || base < 0 || math.Trunc(base) != base || base > 9007199254740991 {
		return 0, fmt.Errorf("缺少或无效的编排 rev")
	}
	prev := int64(0)
	raw, readErr := ReadArrangement(projectID)
	if readErr != nil {
		return 0, readErr
	}
	if len(raw) > 0 {
		old := decodeArrangement(raw)
		if old == nil {
			return 0, fmt.Errorf("现有编排数据损坏，请先恢复备份")
		}
		prev = intField(old, "rev")
	}
	if int64(base) != prev {
		return prev, &ArrangementConflict{Rev: prev, Arrangement: raw}
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
	if original, err := os.ReadFile(file); err == nil {
		var previous map[string]any
		if json.Unmarshal(original, &previous) != nil {
			return fmt.Errorf("现有编排文件损坏，拒绝覆盖")
		}
		if version, _ := previous["version"].(float64); version < 2 {
			f, err := os.OpenFile(file+".before-v2-migration", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				_, err = f.Write(original)
				closeErr := f.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			} else if !os.IsExist(err) {
				return err
			}
		}
	}
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
