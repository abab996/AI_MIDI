"""集中配置模块。

所有可配置项(API key、模型、路径、默认参数)统一在此定义,
其他模块通过 `from config import ...` 引用,避免硬编码散落各处。
"""
import json
import logging
from pathlib import Path

# ===== 路径 =====
# 项目根目录 = 本文件所在目录,所有路径基于此,
# 保证无论从哪个工作目录启动程序,路径都正确。
PROJECT_ROOT: Path = Path(__file__).resolve().parent

INPUT_MIDI: Path = PROJECT_ROOT / "input" / "in.mid"   # 待解析的输入 MIDI
OUTPUT_DIR: Path = PROJECT_ROOT / "output"             # 文本/结果输出目录
OUTPUT_MIDI: Path = OUTPUT_DIR / "output.mid"        # 生成的 MIDI 输出
DOING_DIR: Path = PROJECT_ROOT / "doing"               # 中间产物目录
DOING_OUTPUT_TXT: Path = DOING_DIR / "midi_output.txt" # 解析后的 note_table 文本
PROJECTS_DIR: Path = PROJECT_ROOT / "projects"         # 多轮对话项目存储目录
DRAFT_FILENAME: str = "draft.txt"                       # 项目对话输入框草稿文件名

# ===== Web 服务 =====
SERVER_PORT: int = 7860                                 # FastAPI 服务端口
WEB_DIR: Path = PROJECT_ROOT / "web"                    # 静态前端目录(工作台/对话/设置页)

# ===== 日志系统 =====
_LOG_DIR = OUTPUT_DIR
_LOG_FILE = _LOG_DIR / "ai_midi.log"


def _setup_logging() -> None:
    """Configure logging with fallback to console-only if file logging fails."""
    try:
        _LOG_DIR.mkdir(exist_ok=True)
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s [%(levelname)s] %(name)s - %(message)s",
            handlers=[
                logging.FileHandler(_LOG_FILE, encoding="utf-8"),
                logging.StreamHandler(),
            ],
        )
    except OSError:
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s [%(levelname)s] %(name)s - %(message)s",
            handlers=[logging.StreamHandler()],
        )
        logging.exception("文件日志配置失败,回退到仅控制台日志")


_setup_logging()
logger = logging.getLogger("ai_midi")

# ===== DeepSeek / OpenAI / Gemini API =====
BASE_URL: str = "https://api.deepseek.com"
API_PATH: str = ""
MODEL: str = "deepseek-v4-pro"

# 用户设置持久化文件(API key、模型、生成参数等统一存此)。
SETTINGS_FILE: Path = PROJECT_ROOT / "settings.json"


def load_settings() -> dict:
    """从 settings.json 加载用户设置;文件不存在或损坏时返回空字典。"""
    if not SETTINGS_FILE.exists():
        return {}
    try:
        return json.loads(SETTINGS_FILE.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, UnicodeDecodeError):
        logger.exception("读取设置文件失败")
        return {}


def save_settings(settings: dict) -> None:
    """把设置写入 settings.json(目录不存在时自动创建)。"""
    try:
        SETTINGS_FILE.write_text(
            json.dumps(settings, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    except (OSError, TypeError):
        logger.exception("写入设置文件失败")
        raise


def get_api_key() -> str:
    """返回 settings.json 中保存的 API key;不存在则返回空字符串。"""
    return (load_settings().get("api_key") or "").strip()


# ===== base_url 安全验证 =====
# 仅强制 https + 443 端口,不限制域名,以便接入任意 OpenAI 兼容格式的服务商。
# （早期版本曾内置域名白名单,已按用户要求移除。）
# Gemini 的 OpenAI 兼容接口需要额外拼接 API 路径(/v1beta/openai),
# 且支持 thinking 扩展参数;其他服务商一律走标准 OpenAI 格式,不拼接 api_path。
GEMINI_BASE_URL_HOST: str = "generativelanguage.googleapis.com"


def is_gemini_provider(base_url: str) -> bool:
    """判断 base_url 是否指向 Gemini 的 OpenAI 兼容接口。"""
    from urllib.parse import urlparse

    raw = base_url.strip()
    if not raw:
        return False
    try:
        parsed = urlparse(raw if "://" in raw else f"https://{raw}")
    except ValueError:
        return False
    return (parsed.hostname or "").lower() == GEMINI_BASE_URL_HOST


def validate_base_url(url: str, api_path: str = "") -> str:
    """验证 base_url 并返回规范化后的 URL;不安全时抛出 ValueError。

    规则:
      - scheme 必须为 https
      - 端口必须为 443（或省略）
      - 禁止 query string 和 fragment
      - api_path 仅在 Gemini 服务商时拼接(其 OpenAI 兼容接口需 /v1beta/openai 路径);
        其他服务商按标准 OpenAI 格式使用 base_url 原路径,忽略 api_path
    """
    from urllib.parse import urlparse

    raw = url.strip()
    if not raw:
        raise ValueError("base_url 不能为空")

    parsed = urlparse(raw)

    if parsed.scheme != "https":
        raise ValueError(f"base_url 必须使用 https 协议: {parsed.scheme}")

    host = (parsed.hostname or "").lower()
    if not host:
        raise ValueError("base_url 缺少主机名")

    if parsed.port is not None and parsed.port != 443:
        raise ValueError(f"base_url 端口必须为 443（标准 HTTPS 端口）: {parsed.port}")

    if parsed.params:
        raise ValueError(f"base_url 不能包含路径参数: {parsed.params}")

    if parsed.query:
        raise ValueError(f"base_url 不能包含查询参数: {parsed.query}")

    if parsed.fragment:
        raise ValueError(f"base_url 不能包含片段标识符: {parsed.fragment}")

    existing_path = parsed.path.rstrip("/") if parsed.path and parsed.path != "/" else ""

    # api_path 是 Gemini 的专属适配字段,仅对 Gemini 生效
    combined_path = existing_path
    if host == GEMINI_BASE_URL_HOST:
        raw_api_path = api_path.strip()
        if raw_api_path:
            path_part = raw_api_path if raw_api_path.startswith("/") else f"/{raw_api_path}"
            path_part = path_part.rstrip("/")
            combined_path = f"{existing_path}{path_part}"

    return f"https://{host}{combined_path}"


# ===== MIDI 默认参数 =====
DEFAULT_BPM: int = 120
DEFAULT_TIME_SIGNATURE: str = "4/4"
TICKS_PER_BEAT: int = 480

# ===== MIDI 音高/力度/演奏限制 =====
NOTES_PER_OCTAVE: int = 12
NOTE_NUMBER_MIN: int = 0
NOTE_NUMBER_MAX: int = 127
MIN_VELOCITY: int = 0
MAX_VELOCITY: int = 127
BPM_MIN: int = 1
BPM_MAX: int = 600

# ===== 数值精度 =====
ROUND_DECIMALS: int = 6
DANGLING_EPSILON: float = 1 / TICKS_PER_BEAT

# ===== 超时默认值 =====
DEFAULT_TIMEOUT_SECONDS: float = 60.0
DEFAULT_CONNECT_TIMEOUT_SECONDS: float = 10.0
# 多轮对话(工具调用)流式请求的读超时：思考模型(如 step-3.7-flash, thinking=max)
# 可能长时间不输出首字节，60s 默认值会误杀，故单独放宽。
CHAT_TIMEOUT_SECONDS: float = 300.0
MCP_RESPONSE_TIMEOUT: float = 30.0
MCP_LIST_TIMEOUT: float = 15.0
MCP_STARTUP_SLEEP: float = 0.5
MCP_SHUTDOWN_TIMEOUT: int = 5

# ===== Web UI 默认值 =====
RESULT_BOX_LINES: int = 40
RESULT_BOX_MAX_LINES: int = 80
DEFAULT_MAX_TOKENS: int = 4096
MAX_TOKENS_MIN: int = 1
MAX_TOKENS_MAX: int = 1000000
CHAT_READY_TIMEOUT: float = 15.0
CHAT_READY_POLL_INTERVAL: float = 0.3
CHAT_START_TIMEOUT: float = 10.0

# ===== 多轮对话 / Context 限制 =====
MAX_TOOL_ROUNDS: int = 10
MAX_CONTEXT_CHARS: int = 400_000
COMPACT_KEEP_RECENT_MESSAGES: int = 3
SUMMARY_USER_TRUNCATE_CHARS: int = 100
SUMMARY_AI_TRUNCATE_CHARS: int = 80
