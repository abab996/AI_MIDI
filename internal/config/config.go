package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	// LibraryUserDir 用户自定义乐理知识文件目录（Library/user）：设置页
	// 「知识库」分区可管理，AI 清单中以 LibraryUserPrefix 与内置文件区分
	LibraryUserDir = filepath.Join(LibraryDir, "user")
	WebDir         = filepath.Join(ProjectRoot, "frontend")
	SettingsFile   = filepath.Join(ProjectRoot, "settings.json")
	LogFile        = filepath.Join(OutputDir, "ai_midi.log")
)

const (
	DraftFilename = "draft.txt"
	ServerPort    = 7860
	WindowTitle   = "AI_MIDI · AI 编曲助手"
	// LibraryUserPrefix ListLibraryFiles 中用户自定义知识文件的文件名前缀
	// （如 "user/爵士和声.md"），同时是 read_library_file 工具读取用户文件
	// 时使用的相对路径参数
	LibraryUserPrefix = "user/"
	// UpdateManifestURL 更新清单地址（R2 桶绑定的公开自定义域名）。
	// 应用启动时拉取并比较版本；清单必须公开可读（S3 API 端点不行）。
	UpdateManifestURL = "https://aimidi-r2.baimoo.top/update.json"
)

// AppVersion 应用版本：main 启动时从 wails.json 注入（与打包配置同源，
// 避免双份维护）；未注入时（单测/浏览器降级）显示 dev。
var AppVersion = "dev"

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
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// rel 以 ".." 开头表示在 tmp 之外
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
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
