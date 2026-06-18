"""集中配置模块。

所有可配置项(API key、模型、路径、默认参数)统一在此定义,
其他模块通过 `from config import ...` 引用,避免硬编码散落各处。
"""
import os
import sys
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

# ===== DeepSeek API =====
BASE_URL: str = "https://api.deepseek.com"
MODEL: str = "deepseek-v4-pro"

DEEPSEEK_API_KEY: str | None = os.environ.get("DEEPSEEK_API_KEY")


def require_api_key() -> str:
    """返回 API key;若环境变量未配置,打印明确错误并退出。"""
    if not DEEPSEEK_API_KEY:
        print("错误:未检测到环境变量 DEEPSEEK_API_KEY。")
        print("请先设置该环境变量,或在本项目根目录创建 .env 文件:")
        print("    DEEPSEEK_API_KEY=sk-你的key")
        print("(可参考 .env.example)")
        sys.exit(1)
    return DEEPSEEK_API_KEY


# ===== MIDI 默认参数 =====
DEFAULT_BPM: int = 120
DEFAULT_TIME_SIGNATURE: str = "4/4"
TICKS_PER_BEAT: int = 480
