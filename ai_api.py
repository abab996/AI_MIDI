"""DeepSeek LLM 调用模块。

把 note_table 文本发给 DeepSeek(扮演乐理专家),完成四类任务:
配和弦、翻译歌词、设计转音、其他要求。

所有函数共享同一个 system prompt 与调用通道(_chat),
各自只负责拼装对应的 user content。
"""
from openai import OpenAI
import openai

import config

# ===== 客户端 =====
client = OpenAI(
    api_key=config.require_api_key(),
    base_url=config.BASE_URL,
)

# ===== 共享 system prompt =====
# 把模型设定为乐理专家,并定义 note_table 文本格式。
SYSTEM_PROMPT: str = (
    "现在你是一位精通乐理的音乐人，你需要根据用户的要求解决用户的问题。\n"
    "由于无法直接上传midi文件，我们会使用类似midi文件的\"note_table\"格式来记录音符信息，"
    "以下位\"note_table\"的格式介绍：\n"
    "[note: \"<音符键名>\", velocity: \"<音符力度>\", start: \"<音符的开始时间（拍）>\", "
    "end: \"<音符的结束时间（拍）>\" ]\n"
    "示例 ：\n"
    "[note: \"C4\", velocity: \"80\", start: \"1\", end: \"2\" ] \n"
    "表示音符对应的按键是C4，演奏力度80，时间是第一拍到第二拍。 "
)

# 要求最终回答「仅含 note_table 格式数据、必须完整、禁止额外内容」的统一后缀。
NOTE_TABLE_ONLY_SUFFIX: str = (
    "，必须给出完整可用的音符数据（和弦数据），禁止只给出部分示例,"
    "且最终回答中仅包含\"note_table\"格式的音符数据,禁止掺杂其它无关内容。"
)


def _chat(user_content: str) -> str:
    """统一调用 DeepSeek。

    所有公开功能函数都通过本函数与模型通信,
    在此集中处理 reasoning/thinking 参数与异常。
    失败时打印友好中文错误并返回空字符串,而非让程序崩溃。
    """
    try:
        response = client.chat.completions.create(
            model=config.MODEL,
            messages=[
                {"role": "system", "content": SYSTEM_PROMPT},
                {"role": "user", "content": user_content},
            ],
            stream=False,
            reasoning_effort="max",
            extra_body={"thinking": {"type": "enabled"}},
        )
    except openai.APIError as e:
        print(f"调用 AI API 时发生错误(APIError):{e}")
        return ""
    except openai.OpenAIError as e:
        print(f"调用 AI API 时发生错误:{e}")
        return ""

    result: str = response.choices[0].message.content
    print(result)
    return result


def add_chord(note_table, bpm, time_signature, requirements) -> str:
    """配和弦:给一段旋律配上和弦,返回 note_table 格式字符串。"""
    print("正在调用AI API进行配和弦...")
    user_content = (
        f"音符数据：{note_table}，BPM：{bpm}，拍号：{time_signature}，"
        f"现在你需要给这段旋律配上适合的和弦。"
        f"注意最终回答内只能包含\"note_table\"格式数据，禁止出现任何额外内容或不符合格式的内容"
        f"{NOTE_TABLE_ONLY_SUFFIX}。有如下要求：{requirements}"
    )
    return _chat(user_content)


def translate_lyrics(note_table, lyrics, bpm, time_signature,
                     original_language, target_language) -> str:
    """翻译歌词:把原语言歌词翻译成目标语言并贴合人声旋律。"""
    print("正在翻译歌词...")
    user_content = (
        f"音符数据：{note_table}，歌词数据：{lyrics}，BPM：{bpm}，拍号：{time_signature}，"
        f"现在你得到的是人声的旋律和一段{original_language}歌词。"
        f"你需要把这段{original_language}歌词翻译成{target_language}。"
        f"并且使翻译后的歌词能够与人声旋律完美贴合。"
        f"注意最终回答中只能包含翻译后的歌词原文"
        f"（如果翻译的目标语言是日语，请在给出的翻译歌词后面列出其对应的平假名）。"
        f"有如下要求："
    )
    return _chat(user_content)


def design_melisma(note_table, lyrics, bpm, time_signature, requirements) -> str:
    """设计转音:为旋律生成装饰性的转音/花腔,返回 note_table 格式字符串。"""
    print("正在调用AI API设计转音...")
    user_content = (
        f"音符数据：{note_table}，歌词数据：{lyrics}，BPM：{bpm}，拍号：{time_signature}，"
        f"你现在需要帮我设计转音"
        f"{NOTE_TABLE_ONLY_SUFFIX}。有如下要求：{requirements}"
    )
    return _chat(user_content)


def other_requirements(note_table, lyrics, bpm, time_signature,
                       requirements, note_output) -> str:
    """其他要求:自由任务。note_output 决定输出 note_table(MIDI)还是纯文本回答。"""
    print("AI正在调用中，请稍候...")
    base = (
        f"音符数据：{note_table}，歌词数据：{lyrics}，BPM：{bpm}，拍号：{time_signature}，"
        f"现在你可能没有得到有效的音符或歌词数据（也有可能得到了有效数据），"
    )
    if note_output:
        user_content = (
            f"{base}但是你现在需要输出音符文件，请根据以下要求完成任务：{requirements}。"
            f"请在最终回复中严格按照\"note_table\"格式输出音符数据"
            f"{NOTE_TABLE_SUFFIX}。"
        )
    else:
        user_content = (
            f"{base}但是你现在不用输出音符文件，请根据以下要求完成任务：{requirements}，并给出回答"
        )
    return _chat(user_content)
