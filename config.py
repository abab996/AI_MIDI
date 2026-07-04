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
OUTPUT_MIDI: Path = PROJECT_ROOT / "output.mid"        # 生成的 MIDI 输出
DOING_DIR: Path = PROJECT_ROOT / "doing"               # 中间产物目录
DOING_OUTPUT_TXT: Path = DOING_DIR / "midi_output.txt" # 解析后的 note_table 文本
PROJECTS_DIR: Path = PROJECT_ROOT / "projects"         # 多轮对话项目存储目录

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
    except Exception:
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s [%(levelname)s] %(name)s - %(message)s",
            handlers=[logging.StreamHandler()],
        )


_setup_logging()
logger = logging.getLogger("ai_midi")

# ===== DeepSeek API =====
BASE_URL: str = "https://api.deepseek.com"
MODEL: str = "deepseek-v4-pro"

# 用户设置持久化文件(API key、模型、生成参数等统一存此)。
SETTINGS_FILE: Path = PROJECT_ROOT / "settings.json"


def load_settings() -> dict:
    """从 settings.json 加载用户设置;文件不存在或损坏时返回空字典。"""
    if not SETTINGS_FILE.exists():
        return {}
    try:
        return json.loads(SETTINGS_FILE.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        logger.exception("读取设置文件失败")
        return {}


def save_settings(settings: dict) -> None:
    """把设置写入 settings.json(目录不存在时自动创建)。"""
    try:
        SETTINGS_FILE.write_text(
            json.dumps(settings, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    except Exception:  # noqa: BLE001
        logger.exception("写入设置文件失败")
        raise


def get_api_key() -> str:
    """返回 settings.json 中保存的 API key;不存在则返回空字符串。"""
    return (load_settings().get("api_key") or "").strip()


# ===== MIDI 默认参数 =====
DEFAULT_BPM: int = 120
DEFAULT_TIME_SIGNATURE: str = "4/4"
TICKS_PER_BEAT: int = 480
