package tasks

import (
	"log/slog"
)

// TaskDetach 将未完成的对话任务生成器移交后台驱动 Goroutine
func TaskDetach(taskID string, driverFunc func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("后台任务驱动发生异常", "taskID", taskID, "panic", r)
			}
			TaskFinish(taskID)
		}()

		driverFunc()
	}()
}
