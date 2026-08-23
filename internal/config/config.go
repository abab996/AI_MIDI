package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// ===== 路径常量 =====
var (
	// ProjectRoot 为可执行文件所在目录或当前工作目录
	ProjectRoot = getProjectRoot()

	InputMidi      = filepath.Join(ProjectRoot, "input", "in.mid")
	OutputDir      = filepath.Join(ProjectRoot, "output")
	OutputMidi     = filepath.Join(OutputDir, "output.mid")
	DoingDir       = filepath.Join(ProjectRoot, "doing")
	DoingOutputTxt = filepath.Join(DoingDir, "midi_output.txt")
	ProjectsDir    = filepath.Join(ProjectRoot, "projects")
	LibraryDir     = filepath.Join(ProjectRoot, "Library")
	WebDir         = filepath.Join(ProjectRoot, "frontend")
	SettingsFile   = filepath.Join(ProjectRoot, "settings.json")
	LogFile        = filepath.Join(OutputDir, "ai_midi.log")
)

const (
	DraftFilename = "draft.txt"
	ServerPort    = 7860
	WindowTitle   = "AI_MIDI · AI 编曲助手"
)

// ===== DeepSeek / OpenAI / Gemini API 默认值 =====
const (
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultAPIPath = ""
	DefaultModel   = "deepseek-v4-pro"

	GeminiBaseURLHost = "generativelanguage.googleapis.com"
)

// ===== MIDI 默认参数 =====
const (
	DefaultBPM           = 120
	DefaultTimeSignature = "4/4"
	TicksPerBeat         = 480

	NotesPerOctave = 12
	NoteNumberMin  = 0
	NoteNumberMax  = 127
	MinVelocity    = 0
	MaxVelocity    = 127
	BPMMin         = 1
	BPMMax         = 600

	RoundDecimals   = 6
	DanglingEpsilon = 1.0 / float64(TicksPerBeat)
)

// ===== 超时与并发限制 =====
const (
	DefaultTimeoutSeconds        = 60
	DefaultConnectTimeoutSeconds = 10
	ChatTimeoutSeconds           = 300
	MCPResponseTimeoutSeconds    = 30

	DefaultMaxTokens = 4096
	MaxTokensMin     = 1
	MaxTokensMax     = 1000000

	MaxToolRounds             = 10
	MaxContextChars           = 400000
	CompactKeepRecentMessages = 3
	SummaryUserTruncateChars  = 100
	SummaryAITruncateChars    = 80
)

func getProjectRoot() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		// 开发阶段若在 go run 产生的临时目录，回退到当前工作目录
		if !isTempDir(dir) {
			return dir
		}
	}
	cwd, err := os.Getwd()
	if err == nil {
		return cwd
	}
	return "."
}

func isTempDir(dir string) bool {
	clean := filepath.Clean(dir)
	tmp := filepath.Clean(os.TempDir())
	rel, err := filepath.Rel(tmp, clean)
	if err == nil && len(rel) > 0 && rel != "." && rel[:2] != ".." {
		return true
	}
	return false
}

// SetupLogging 初始化 slog 文件与控制台双写日志
func SetupLogging() {
	_ = os.MkdirAll(OutputDir, 0755)
	logF, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	var writer io.Writer = os.Stdout
	if err == nil {
		writer = io.MultiWriter(os.Stdout, logF)
	}

	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))
}
