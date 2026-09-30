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
	LogFile        = filepath.Join(OutputDir, "melodymate.log")
)

const (
	DraftFilename = "draft.txt"
	ServerPort    = 7860
	WindowTitle   = "MelodyMate · AI 编曲助手"
	// LibraryUserPrefix ListLibraryFiles 中用户自定义知识文件的文件名前缀
	// （如 "user/爵士和声.md"），同时是 read_library_file 工具读取用户文件
	// 时使用的相对路径参数
	LibraryUserPrefix = "user/"
	// UpdateManifestURL 首选更新清单。新域名尚未交付，先占位；
	// 拉取时按 UpdateManifestURLs 失败回退到仍在服务的旧桶。
	UpdateManifestURL = "https://melodymate-r2.baimoo.top/update.json"
)

// UpdateManifestURLs 按顺序尝试。第一项不可达时继续下一项，
// 避免改名后旧安装发现不了新版本。
var UpdateManifestURLs = []string{
	UpdateManifestURL,
	"https://aimidi-r2.baimoo.top/update.json",
}

// UpdateManifestHosts 允许承载更新清单与安装包的主机白名单。
// 清单里的 downloads.windows 是远程数据，若不校验主机，清单被改写（或
// 占位域名落入他人之手）就能把安装包指向任意地址；下载后会以静默参数
// 在本机运行，因此主机必须收敛到自有域名。
// 维护约束：新增/更换清单域名时，UpdateManifestURLs 与这里必须同时更新。
var UpdateManifestHosts = []string{
	"melodymate-r2.baimoo.top",
	"aimidi-r2.baimoo.top",
}

// AppVersion 应用版本：main 启动时从 wails.json 注入（与打包配置同源，
// 避免双份维护）；未注入时（单测/浏览器降级）显示 dev。
var AppVersion = "dev"

// ===== DeepSeek / OpenAI / Gemini API 默认值 =====
const (
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultAPIPath = ""
	DefaultModel   = "deepseek-v4-pro"

	GeminiBaseURLHost = "generativelanguage.googleapis.com"

	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
	ProtocolGemini    = "gemini"
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

// isTempDir 判断 exe 目录是否属于开发/构建临时区：os.TempDir() 下的
// go run 临时目录，以及 GOCACHE 下的 go run 编译缓存目录
// （%LOCALAPPDATA%\go-build\<hash>-d，位于 Temp 之外）。两者都是
// 开发态产物，真实 install 目录在此之外。
func isTempDir(dir string) bool {
	clean := filepath.Clean(dir)
	for _, root := range []string{os.TempDir(), buildCacheDir()} {
		rootClean := filepath.Clean(root)
		if rootClean == "" {
			continue
		}
		rel, err := filepath.Rel(rootClean, clean)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		// rel 以 ".." 开头表示在 root 之外，继续尝试下一个根
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// buildCacheDir 返回 Go 构建缓存根（go run 的 exe 缓存于其子目录）。
// 优先环境变量 GOCACHE，回退 os.UserCacheDir()/go-build；两者都取不到
// 时返回空串（调用方跳过该根）。
func buildCacheDir() string {
	if dir := os.Getenv("GOCACHE"); dir != "" {
		return dir
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "go-build")
	}
	return ""
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
