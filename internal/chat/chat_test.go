package chat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
	"aimidi/internal/project"
)

func TestNormalizeQuestions(t *testing.T) {
	// 1. nil
	if res := NormalizeQuestions(nil); res != nil {
		t.Fatalf("expected nil, got %v", res)
	}

	// 2. valid []any
	rawAny := []any{
		map[string]any{
			"question": "你想要什么风格？",
			"header":   "风格",
			"options": []any{
				map[string]any{"label": "流行", "description": "流行乐风格"},
				map[string]any{"label": "古风", "description": "中国古典民乐"},
			},
			"multiSelect": false,
		},
	}
	normAny := NormalizeQuestions(rawAny)
	if len(normAny) != 1 {
		t.Fatalf("expected 1 question, got %d", len(normAny))
	}
	if normAny[0]["question"] != "你想要什么风格？" || normAny[0]["header"] != "风格" {
		t.Fatalf("unexpected question: %+v", normAny[0])
	}
	opts, ok := normAny[0]["options"].([]map[string]any)
	if !ok || len(opts) != 2 {
		t.Fatalf("unexpected options: %+v", normAny[0]["options"])
	}

	// 3. valid []map[string]any
	rawMap := []map[string]any{
		{
			"question": "速度 BPM 是多少？",
			"header":   "速度",
			"options": []map[string]any{
				{"label": "慢速 80", "description": "抒情慢歌"},
				{"label": "中速 120", "description": "欢快标准"},
			},
			"multiSelect": true,
		},
	}
	normMap := NormalizeQuestions(rawMap)
	if len(normMap) != 1 || normMap[0]["multiSelect"] != true {
		t.Fatalf("unexpected question: %+v", normMap)
	}

	// 4. invalid (insufficient options or empty header)
	rawInvalid := []any{
		map[string]any{
			"question": "问题？",
			"header":   "",
			"options": []any{
				map[string]any{"label": "A"},
				map[string]any{"label": "B"},
			},
		},
		map[string]any{
			"question": "问题2？",
			"header":   "标签",
			"options": []any{
				map[string]any{"label": "A"},
			},
		},
	}
	if res := NormalizeQuestions(rawInvalid); len(res) != 0 {
		t.Fatalf("expected 0 valid questions, got %d", len(res))
	}
}

func TestFormatAnswerResult(t *testing.T) {
	questions := []map[string]any{
		{
			"question": "请选择风格？",
			"options": []map[string]any{
				{"label": "流行", "description": "流行风格"},
				{"label": "摇滚", "description": "摇滚风格"},
			},
		},
		{
			"question": "请选择速度？",
			"options": []map[string]any{
				{"label": "120", "description": "标准速度"},
			},
		},
	}

	// 1. Skip / empty
	resEmpty := FormatAnswerResult(questions, nil)
	if resEmpty != "用户未回答提问（选择跳过），请基于现有信息按你的专业判断继续。" {
		t.Fatalf("unexpected empty answer result: %s", resEmpty)
	}

	// 2. Answered
	answers := []any{
		map[string]any{
			"question_index": float64(0),
			"selected":       []any{"流行"},
			"other":          "",
		},
		map[string]any{
			"question_index": float64(1),
			"selected":       []any{},
			"other":          "135 自定义",
		},
	}

	res := FormatAnswerResult(questions, answers)
	if res == "" {
		t.Fatalf("expected non-empty formatted answer result")
	}
	if !contains(res, "1. 请选择风格？") || !contains(res, "→ 流行（流行风格）") {
		t.Fatalf("formatted result missing question 1: %s", res)
	}
	if !contains(res, "2. 请选择速度？") || !contains(res, "→ 自定义: 135 自定义") {
		t.Fatalf("formatted result missing question 2: %s", res)
	}
}

func contains(s, substr string) bool {
	return filepath.ToSlash(s) != "" && len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && searchSubstr(s, substr)))
}

func searchSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func setupChatTest(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("", "aimidi_chat_test_*")
	if err != nil {
		t.Fatal(err)
	}

	config.ProjectsDir = filepath.Join(tmpDir, "projects")
	config.SettingsFile = filepath.Join(tmpDir, "settings.json")
	_ = os.MkdirAll(config.ProjectsDir, 0755)

	meta, err := project.CreateProject("测试提问项目")
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		DropSession(meta.ID)
		_ = os.RemoveAll(tmpDir)
	}

	return meta.ID, cleanup
}

func TestQuestionLifecycle(t *testing.T) {
	projectID, cleanup := setupChatTest(t)
	defer cleanup()

	s := GetSession(projectID)
	tid := s.CurrentTaskID
	st := s.GetTaskState(tid)

	questions := []map[string]any{
		{
			"question": "你需要什么风格？",
			"header":   "风格",
			"options": []map[string]any{
				{"label": "流行", "description": "流行"},
				{"label": "摇滚", "description": "摇滚"},
			},
			"multiSelect": false,
		},
	}

	// 1. SetPendingQuestion
	qid := SetPendingQuestion(projectID, tid, "call_test123", questions, nil)
	if qid == "" {
		t.Fatalf("expected valid question ID")
	}

	if st.PendingQuestion == nil {
		t.Fatalf("expected st.PendingQuestion to be set")
	}

	// 2. ChatDisplay has question
	st.ChatDisplay = append(st.ChatDisplay, map[string]any{
		"role":        "assistant",
		"type":        "question",
		"question_id": qid,
		"questions":   questions,
		"status":      "pending",
	})

	// 3. MarkQuestionBlock as answered
	answers := []any{
		map[string]any{
			"question_index": 0,
			"selected":       []any{"流行"},
		},
	}
	MarkQuestionBlock(st.ChatDisplay, qid, answers)

	lastMsg := st.ChatDisplay[len(st.ChatDisplay)-1]
	if lastMsg["status"] != "answered" {
		t.Fatalf("expected status = answered, got %v", lastMsg["status"])
	}

	// 4. SkipPendingQuestion
	st.PendingQuestion = map[string]any{
		"question_id":     qid,
		"tool_call_id":    "call_test123",
		"questions":       questions,
		"remaining_calls": nil,
		"task_id":         tid,
	}
	SkipPendingQuestion(st, projectID, tid, s.MidiFiles, false)

	if st.PendingQuestion != nil {
		t.Fatalf("expected PendingQuestion to be nil after skip")
	}
	if lastMsg["status"] != "skipped" {
		t.Fatalf("expected status = skipped, got %v", lastMsg["status"])
	}
}

func TestChatStreamAndAnswerStreamMock(t *testing.T) {
	projectID, cleanup := setupChatTest(t)
	defer cleanup()

	// 模拟上游 LLM 返回 ask_user_question 工具调用的 SSE 数据
	sseChunk1 := "data: {\"choices\":[{\"delta\":{\"content\":\"我需要先向你确认几个创作要素。\"}}]}\n\n"
	sseChunk2 := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_ask_1\",\"type\":\"function\",\"function\":{\"name\":\"ask_user_question\",\"arguments\":\"{\\\"questions\\\":[{\\\"question\\\":\\\"请选择主奏乐器？\\\",\\\"header\\\":\\\"乐器\\\",\\\"options\\\":[{\\\"label\\\":\\\"钢琴\\\",\\\"description\\\":\\\"优雅\\\"},{\\\"label\\\":\\\"吉他\\\",\\\"description\\\":\\\"清脆\\\"}]}]}\"}}]}}]}\n\n"
	sseChunk3 := "data: [DONE]\n\n"

	// 模拟回答后 LLM 最终回复
	sseFinal1 := "data: {\"choices\":[{\"delta\":{\"content\":\"好的，为你使用钢琴制作完成了。\"}}]}\n\n"
	sseFinal2 := "data: [DONE]\n\n"

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "text/event-stream")
		if callCount == 1 {
			_, _ = fmt.Fprint(w, sseChunk1)
			_, _ = fmt.Fprint(w, sseChunk2)
			_, _ = fmt.Fprint(w, sseChunk3)
		} else {
			_, _ = fmt.Fprint(w, sseFinal1)
			_, _ = fmt.Fprint(w, sseFinal2)
		}
	}))
	defer ts.Close()

	// 保存配置指向 mock server
	config.SaveSettings(config.Settings{
		APIKey:  "mock-key",
		BaseURL: ts.URL,
		Model:   "mock-model",
	})

	var events []map[string]any
	callback := func(event map[string]any) error {
		events = append(events, event)
		return nil
	}

	// 1. 发送第一条消息，触发 ask_user_question
	err := ChatStream(projectID, "帮我写一首乐曲", false, false, nil, callback)
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}

	s := GetSession(projectID)
	st := s.GetTaskState("")

	if st.PendingQuestion == nil {
		t.Fatalf("expected pending question to be active")
	}

	qid, _ := st.PendingQuestion["question_id"].(string)
	if qid == "" {
		t.Fatalf("missing question_id in pending question")
	}

	// 验证 chat_display 中是否保留了 type: question 的卡片！
	foundQuestionCard := false
	for _, m := range st.ChatDisplay {
		if t, _ := m["type"].(string); t == "question" {
			if m["status"] == "pending" && m["question_id"] == qid {
				foundQuestionCard = true
				break
			}
		}
	}
	if !foundQuestionCard {
		t.Fatalf("ChatDisplay is missing pending question card: %+v", st.ChatDisplay)
	}

	// 2. 模拟用户回答提问
	events = nil
	userAnswers := []any{
		map[string]any{
			"question_index": 0,
			"selected":       []any{"钢琴"},
		},
	}

	err = AnswerStream(projectID, qid, userAnswers, callback)
	if err != nil {
		t.Fatalf("AnswerStream failed: %v", err)
	}

	// 验证回答后，问题卡片被标记为 answered，并且 AI 给出了最终回复
	foundAnsweredCard := false
	foundFinalReply := false
	for _, m := range st.ChatDisplay {
		if t, _ := m["type"].(string); t == "question" {
			if m["status"] == "answered" && m["question_id"] == qid {
				foundAnsweredCard = true
			}
		}
		if r, _ := m["role"].(string); r == "assistant" {
			if c, _ := m["content"].(string); contains(c, "好的，为你使用钢琴制作完成了。") {
				foundFinalReply = true
			}
		}
	}

	if !foundAnsweredCard {
		t.Fatalf("ChatDisplay question card was not updated to answered: %+v", st.ChatDisplay)
	}
	if !foundFinalReply {
		t.Fatalf("ChatDisplay missing final AI reply after answering: %+v", st.ChatDisplay)
	}
}

func TestCompactFullHistoryRoleAlternation(t *testing.T) {
	// 构造多轮历史
	var fullHistory []map[string]any
	for i := 1; i <= 8; i++ {
		fullHistory = append(fullHistory,
			map[string]any{"role": "user", "content": fmt.Sprintf("用户问题 %d", i)},
			map[string]any{"role": "assistant", "content": fmt.Sprintf("AI 回答 %d", i)},
		)
	}

	compacted := CompactFullHistory(fullHistory)
	if len(compacted) >= len(fullHistory) {
		t.Fatalf("期望压缩后消息数量减少，实际得到 %d 条", len(compacted))
	}

	// 验证消息角色严格交替，绝不出现连续 user 消息
	for i := 1; i < len(compacted); i++ {
		rPrev, _ := compacted[i-1]["role"].(string)
		rCurr, _ := compacted[i]["role"].(string)
		if rPrev == "user" && rCurr == "user" {
			t.Fatalf("发现连续两条 user 角色消息在索引 %d 和 %d: %+v", i-1, i, compacted)
		}
	}
}
