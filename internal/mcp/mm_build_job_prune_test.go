package mcp

import (
	"testing"
	"time"
)

// 作业表不能只增不减：反复编译的长会话里会缓慢积累。
func TestPruneMMBuildJobs(t *testing.T) {
	mmBuildJobs.Range(func(k, _ any) bool { mmBuildJobs.Delete(k); return true })
	defer mmBuildJobs.Range(func(k, _ any) bool { mmBuildJobs.Delete(k); return true })

	// 起点要落在 TTL 之内（30 分钟）：否则所有作业都会被 TTL 规则清掉，
	// 测不到"按数量保留最近 N 个"这条规则
	base := time.Now().Add(-10 * time.Minute)
	ids := make([]string, 0, mmBuildJobKeep+7)
	for i := 0; i < mmBuildJobKeep+7; i++ {
		id := "job_" + string(rune('a'+i))
		ids = append(ids, id)
		job := &mmBuildJob{ID: id, PluginID: "p", Started: base.Add(time.Duration(i) * time.Second)}
		if i%2 == 0 {
			job.finish(nil) // 一半是已完成作业
		}
		mmBuildJobs.Store(id, job)
	}
	pruneMMBuildJobs()

	n := 0
	mmBuildJobs.Range(func(_, _ any) bool { n++; return true })
	if n > mmBuildJobKeep {
		t.Fatalf("清理后应不超过 %d 个，实际 %d", mmBuildJobKeep, n)
	}
	if _, ok := mmBuildJobByID(ids[len(ids)-1]); !ok {
		t.Fatal("最新的作业被误删（那会把刚启动的编译丢掉）")
	}

	// 超 TTL 的一律清掉（即使只有它一个）
	stale := &mmBuildJob{ID: "stale_job", Started: time.Now().Add(-2 * mmBuildJobTTL)}
	mmBuildJobs.Store(stale.ID, stale)
	pruneMMBuildJobs()
	if _, ok := mmBuildJobByID("stale_job"); ok {
		t.Fatal("超 TTL 的作业应被清掉")
	}
}
