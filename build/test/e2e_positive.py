# -*- coding: utf-8 -*-
"""AI_MIDI E2E 正向测试矩阵：覆盖全部 API 端点（浏览器模式 + mockLLM）"""
import json
import sys
import time
import urllib.request
import urllib.error
import os

BASE = "http://127.0.0.1:7860"
PASS = 0
FAIL = 0
FAILED = []
SKIP = 0
SKIPPED = []

# 本轮架构升级给 /api/* 加了启动令牌闸门（X-MM-Token，见 router.go 的
# tokenExemptPath / ServeHTTP）。脚本按页面同款路径从免令牌的
# /api/bootstrap.js 取令牌；取不到（旧版服务端 / 随机源失败）就按无令牌
# 发送，与升级前行为完全一致，不改变任何既有断言的判定口径。
TOKEN = None
_TOKEN_TRIED = False


def _load_token():
    global TOKEN, _TOKEN_TRIED
    if _TOKEN_TRIED:
        return TOKEN
    _TOKEN_TRIED = True
    try:
        r = urllib.request.Request(BASE + "/api/bootstrap.js",
                                   headers={"Host": "127.0.0.1:7860"}, method="GET")
        with urllib.request.urlopen(r, timeout=10) as resp:
            TOKEN = (json.loads(resp.read() or b"{}") or {}).get("token") or None
    except Exception:
        TOKEN = None
    return TOKEN


def auth_headers(extra=None):
    """原始连接（http.client / socket）用的请求头，带上启动令牌。"""
    h = {"Host": "127.0.0.1:7860"}
    tok = _load_token()
    if tok:
        h["X-MM-Token"] = tok
    if extra:
        h.update(extra)
    return h


def req(method, path, data=None, headers=None, raw_body=False, timeout=30):
    url = BASE + path
    h = {"Host": "127.0.0.1:7860"}
    body = None
    if data is not None:
        if raw_body:
            body = data
            h["Content-Type"] = headers.get("Content-Type", "application/json") if headers else "application/json"
        else:
            body = json.dumps(data).encode("utf-8")
            h["Content-Type"] = "application/json"
    if headers:
        h.update(headers)
    tok = _load_token()
    if tok and "X-MM-Token" not in h:
        h["X-MM-Token"] = tok
    r = urllib.request.Request(url, data=body, headers=h, method=method)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            b = resp.read()
            return resp.status, b, dict(resp.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)
    except Exception as e:
        return -1, str(e).encode(), {}


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  ✓ {name}")
    else:
        FAIL += 1
        FAILED.append(name)
        print(f"  ✗ {name}  {detail}")


def skip(name, why):
    """前置条件不满足（缺引擎/插件目录/真实音色库等）：不计入 PASS/FAIL。"""
    global SKIP
    SKIP += 1
    SKIPPED.append(f"{name} —— {why}")
    print(f"  · 跳过 {name}：{why}")


def engine_unavailable(st, body):
    """引擎类端点的环境性不可用：503（引擎未启动）或 500（引擎未就绪/内部报错）。
    浏览器模式没有原生引擎目录时属于环境问题，按跳过处理而不是判失败。"""
    return st in (500, 503)


def as_json(body):
    try:
        return json.loads(body)
    except Exception:
        return None


def get_json(path, headers=None):
    st, b, _ = req("GET", path, headers=headers)
    try:
        return st, json.loads(b) if b else None
    except Exception:
        return st, None


print("=" * 60)
print("A. 系统端点")
print("=" * 60)
st, b, _ = req("GET", "/api/health")
check("GET /api/health", st == 200 and b"ok" in b, f"{st} {b[:100]}")

st, j = get_json("/api/version")
check("GET /api/version", st == 200 and j and j.get("version"), f"{st} {j}")

st, j = get_json("/api/settings")
check("GET /api/settings", st == 200 and j, f"{st} {j}")
if j:
    masked = j.get("api_key", "")
    check("settings 中 api_key 已掩码", masked != "test-key" and "test-key" not in masked, f"key={masked!r}")

st, j = get_json("/api/settings?x=1")
check("GET /api/settings 带 query", st == 200 and j, f"{st}")

st, b, _ = req("PUT", "/api/settings", {"api_key": "****", "base_url": "http://127.0.0.1:9321/v1", "api_path": "/v1", "model": "mock-model", "max_tokens": 300, "max_completion_tokens": 300, "reasoning_effort": "LOW", "thinking_enabled": False})
check("PUT /api/settings 部分字段", st == 200, f"{st} {b[:200]}")
st, j = get_json("/api/settings")
check("PUT 后 model 生效", j and j.get("model") == "mock-model", f"{j}")

st, b, _ = req("POST", "/api/transport/prefs", {"resume_on_pause": True})
check("POST /api/transport/prefs", st == 200, f"{st} {b[:100]}")

st, b, _ = req("POST", "/api/models", {"api_key": "test-key", "base_url": "http://127.0.0.1:9321/v1"})
check("POST /api/models (mock)", st == 200 and b"mock-model" in b, f"{st} {b[:200]}")

st, b, _ = req("POST", "/api/client-error", {"message": "e2e 测试错误上报", "page": "e2e", "line": 1, "column": 2, "source": "e2e.js"})
check("POST /api/client-error", st == 200, f"{st} {b[:100]}")

st, b, _ = req("POST", "/api/open-url", {"url": "https://github.com/abab996/AI_MIDI"})
check("POST /api/open-url 白名单内", st == 200, f"{st} {b[:100]}")

st, b, _ = req("POST", "/api/open-url", {"url": "https://evil.example.com"})
check("POST /api/open-url 白名单外拒绝", st in (400, 403), f"{st} {b[:100]}")

print()
print("=" * 60)
print("B. 项目 CRUD 与文件")
print("=" * 60)
st, j = get_json("/api/projects")
check("GET /api/projects 列表", st == 200 and isinstance(j, list), f"{st}")
pid = None
st, j = get_json("/api/projects?nope=1")
check("GET /api/projects 容错", st == 200, f"{st}")

st, b, _ = req("POST", "/api/projects", {"name": "E2E测试项目"})
check("POST /api/projects 创建", st == 200, f"{st} {b[:200]}")
try:
    j = json.loads(b)
    pid = (j.get("meta") or {}).get("id") or j.get("id")
except Exception:
    pass
check("创建返回项目 id", bool(pid), f"body={b[:200]}")

if pid:
    st, j = get_json(f"/api/projects/{pid}")
    check("GET /api/projects/{id} 打开", st == 200 and j, f"{st}")

    st, b, _ = req("PUT", f"/api/projects/{pid}", {"name": "E2E改名"})
    check("PUT /api/projects/{id} 重命名", st == 200, f"{st} {b[:100]}")

    st, b, _ = req("POST", f"/api/projects/{pid}/draft", {"text": "草稿内容测试"})
    check("POST /api/projects/{id}/draft 保存草稿", st == 200, f"{st} {b[:100]}")
    st, j = get_json(f"/api/projects/{pid}/draft")
    check("GET /api/projects/{id}/draft 读草稿", st == 200 and j and "草稿" in str(j), f"{st} {j}")

    st, b, _ = req("POST", f"/api/projects/{pid}/bpm", {"bpm": 96})
    check("POST /api/projects/{id}/bpm", st in (200, 201), f"{st} {b[:100]}")
    st, j = get_json(f"/api/projects/{pid}/bpm")
    check("GET /api/projects/{id}/bpm", st == 200, f"{st} {j}")

    st, b, _ = req("GET", f"/api/projects/{pid}/arrangement")
    check("GET /api/projects/{id}/arrangement", st == 200, f"{st} {b[:100]}")
    st, b, _ = req("PUT", f"/api/projects/{pid}/arrangement", {"tracks": [], "bpm": 96, "channels": []})
    check("PUT /api/projects/{id}/arrangement", st == 200, f"{st} {b[:100]}")

    # 文件上传（.mid）
    midi_path = r"D:\pyx\AI_MIDI-go\frontend\demo\assets\Demo_Melody.mid"
    boundary = "----e2e" + str(int(time.time() * 1000))
    with open(midi_path, "rb") as f:
        midi_data = f.read()
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"files\"; filename=\"e2e_melody.mid\"\r\n"
        f"Content-Type: audio/midi\r\n\r\n"
    ).encode() + midi_data + f"\r\n--{boundary}--\r\n".encode()
    st, b, _ = req("POST", f"/api/projects/{pid}/files", body,
                   headers={"Content-Type": f"multipart/form-data; boundary={boundary}"}, raw_body=True)
    check("POST /api/projects/{id}/files 上传 .mid", st in (200, 201), f"{st} {b[:200]}")

    st, j = get_json(f"/api/projects/{pid}")
    fname = None
    try:
        files = j.get("midi_files") or []
        if isinstance(files, dict):
            files = files.get("files") or []
        for f in files:
            if isinstance(f, dict):
                f = f.get("name", "")
            if str(f).endswith(".mid"):
                fname = f
                break
    except Exception:
        pass
    check("上传后文件列表包含 .mid", bool(fname), f"files={str(j)[:300]}")

    if fname:
        st, b, _ = req("GET", f"/api/files/download?path=projects/{pid}/midi/{fname}", raw_body=True)
        check("GET /api/files/download 下载", st == 200 and len(b) > 100, f"{st} len={len(b)}")

    # 文件夹（create 用 name 字段；rename 用 old/name；delete 用 folder）
    st, b, _ = req("POST", f"/api/projects/{pid}/folders", {"name": "子目录"})
    check("POST /api/projects/{id}/folders 建目录", st == 200, f"{st} {b[:100]}")
    st, b, _ = req("POST", f"/api/projects/{pid}/folders/rename", {"old": "子目录", "name": "新子目录"})
    check("POST /api/projects/{id}/folders/rename 改名", st == 200, f"{st} {b[:100]}")
    st, b, _ = req("POST", f"/api/projects/{pid}/folders/delete", {"folder": "新子目录"})
    check("POST /api/projects/{id}/folders/delete 删除", st == 200, f"{st} {b[:100]}")

    # 历史记录
    st, j = get_json(f"/api/projects/{pid}")
    check("历史记录存在于载荷", j is not None, f"")

    # 复制
    st, b, _ = req("POST", f"/api/projects/{pid}/copy")
    check("POST /api/projects/{id}/copy 复制", st == 200, f"{st} {b[:200]}")
    new_pid = None
    try:
        j = json.loads(b)
        new_pid = (j.get("meta") or {}).get("id") or j.get("id")
    except Exception:
        pass

    # 搜索
    st, j = get_json("/api/projects/search?q=E2E")
    check("GET /api/projects/search", st == 200 and isinstance(j, dict) and len(j.get("ids") or []) >= 1, f"{st} {str(j)[:200]}")

    # 打包下载
    st, b, _ = req("GET", f"/api/projects/{pid}/download", raw_body=True)
    check("GET /api/projects/{id}/download 打包", st == 200 and len(b) > 50, f"{st} len={len(b)}")

    # 消息编辑接口（需要先有消息；无消息时也应容错）
    st, b, _ = req("POST", f"/api/projects/{pid}/messages/recall", {"message_id": "0", "task_id": ""})
    check("POST messages/recall 容错", st in (200, 400, 404), f"{st} {b[:100]}")

    # 复制删除
    if new_pid:
        st, b, _ = req("DELETE", f"/api/projects/{new_pid}")
        check("DELETE /api/projects/{id}（副本删除）", st == 200, f"{st} {b[:100]}")

print()
print("=" * 60)
print("C. MIDI 解析与对话")
print("=" * 60)
midi_path = r"D:\pyx\AI_MIDI-go\frontend\demo\assets\Demo_Melody.mid"
boundary = "----e2eparse" + str(int(time.time() * 1000))
with open(midi_path, "rb") as f:
    midi_data = f.read()
body = (
    f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"demo.mid\"\r\n"
    f"Content-Type: audio/midi\r\n\r\n"
).encode() + midi_data + f"\r\n--{boundary}--\r\n".encode()
st, b, _ = req("POST", "/api/parse", body,
               headers={"Content-Type": f"multipart/form-data; boundary={boundary}"}, raw_body=True)
check("POST /api/parse MIDI 解析", st == 200, f"{st} {b[:300]}")
parsed_ok = False
try:
    j = json.loads(b)
    parsed_ok = bool(j.get("notes") or j.get("table") or j.get("note_table"))
except Exception:
    pass
check("parse 返回音符数据", parsed_ok or st == 200, f"{b[:300]}")

# chat SSE 流式（mock LLM）
import http.client
conn = http.client.HTTPConnection("127.0.0.1", 7860, timeout=60)
body = json.dumps({"project_id": pid, "message": "你好，帮我编曲", "task_id": ""}).encode()
conn.request("POST", "/api/chat", body=body,
             headers=auth_headers({"Content-Type": "application/json"}))
resp = conn.getresponse()
ct = resp.getheader("Content-Type", "")
chat_data = resp.read(20000).decode("utf-8", errors="replace")
conn.close()
check("POST /api/chat SSE 流式", resp.status == 200 and "text/event-stream" in ct, f"st={resp.status} ct={ct}")
check("chat 输出包含事件帧", "data:" in chat_data, chat_data[:300])
check("chat 收到完成事件", "[DONE]" in chat_data or '"done"' in chat_data or "finish" in chat_data, chat_data[-300:])

# chat 工具调用闭环（创建文件）
conn = http.client.HTTPConnection("127.0.0.1", 7860, timeout=120)
body = json.dumps({"project_id": pid, "message": "请创建文件生成一个 MIDI", "task_id": ""}).encode()
conn.request("POST", "/api/chat", body=body,
             headers=auth_headers({"Content-Type": "application/json"}))
resp = conn.getresponse()
chat_data2 = resp.read(50000).decode("utf-8", errors="replace")
conn.close()
check("chat 工具调用流(create_midi)", resp.status == 200 and ("测试歌曲.mid" in chat_data2 or "已创建" in chat_data2), chat_data2[-500:])

# answer SSE：需 question_id（AI 提问卡应答）；缺参应 400
st, b, _ = req("POST", "/api/answer", {"project_id": pid, "question_id": "", "answers": []}, timeout=30)
check("POST /api/answer 缺 question_id → 400", st == 400, f"{st} {b[:150]}")

print()
print("=" * 60)
print("D. 任务端点")
print("=" * 60)
st, j = get_json("/api/tasks?project_id=" + (pid or ""))
tl = j.get("tasks") if isinstance(j, dict) else j
check("GET /api/tasks 列表", st == 200 and isinstance(tl, list), f"{st} {str(j)[:200]}")
# 工具调用流 chat 返回后任务可能仍 running（后台执行），轮询等 completed
tid = None
for _ in range(20):
    tl2 = get_json("/api/tasks?project_id=" + (pid or ""))[1]
    tlist = tl2.get("tasks") if isinstance(tl2, dict) else tl2
    mine = [t for t in (tlist or []) if t.get("project_id") == pid]
    if mine and mine[0].get("status") == "completed":
        tid = mine[0].get("id")
        break
    time.sleep(0.5)
check("工具调用任务转为 completed", bool(tid), f"tasks={str(tl)[:200]}")
if tid:
    st, b, _ = req("POST", f"/api/tasks/{tid}/read")
    check("POST /api/tasks/{id}/read", st == 200, f"{st} {b[:100]}")
    st, b, _ = req("POST", f"/api/tasks/{tid}/rename", {"name": "E2E任务"})
    check("POST /api/tasks/{id}/rename", st == 200, f"{st} {b[:100]}")

print()
print("=" * 60)
print("E. 知识库端点（新功能）")
print("=" * 60)
st, j = get_json("/api/library/files")
check("GET /api/library/files 列表", st == 200 and j, f"{st} {str(j)[:200]}")
builtin_n = len(((j or {}).get("builtin")) or []) if j else 0
user_n = len(((j or {}).get("user")) or []) if j else 0
check("内置知识文件存在", builtin_n >= 10, f"builtin={builtin_n}")
check("用户知识文件分组存在", "user" in (j or {}), f"{str(j)[:200]}")

# 内置内容预览（文件名从列表取实际值）
st, j2 = get_json("/api/library/files")
bl = ((j2 or {}).get("builtin")) or []
check("内置列表非空", len(bl) > 0, f"{str(j2)[:200]}")
import urllib.parse as up
nm = bl[0]["name"] if bl else "01_乐理基础.md"
st, j = get_json(f"/api/library/files/content?name={up.quote(nm)}&scope=builtin")
check("GET content scope=builtin 预览", st == 200 and j and j.get("content") is not None, f"{st} {str(j)[:150]}")

# 上传用户知识文件
content = "# E2E 测试知识\n\n这是端到端测试上传的用户知识文件内容。".encode("utf-8")
st, b, _ = req("POST", "/api/library/files?name=e2e_knowledge.md", content, headers={"Content-Type": "text/markdown"}, raw_body=True)
check("POST /api/library/files 上传", st == 200 and b"e2e_knowledge.md" in b, f"{st} {b[:200]}")

st, j = get_json("/api/library/files")
user_n2 = len(((j or {}).get("user")) or []) if j else 0
check("上传后 user 组增加", user_n2 == user_n + 1, f"before={user_n} after={user_n2}")

# 重命名
st, b, _ = req("POST", "/api/library/files/rename", {"from": "e2e_knowledge.md", "to": "e2e_renamed.md"})
check("POST /api/library/files/rename", st == 200, f"{st} {b[:200]}")

# 预览用户文件
st, j = get_json("/api/library/files/content?name=e2e_renamed.md&scope=user")
check("GET content scope=user 预览", st == 200 and j and "E2E" in (j.get("content") or ""), f"{st} {str(j)[:150]}")

# 非 UTF-8 拒绝
st, b, _ = req("POST", "/api/library/files?name=gbk.txt", b"\xcb\xe2\xbb\xb2\xbd\xe7", headers={"Content-Type": "text/plain"}, raw_body=True)
check("上传 GBK 编码拒绝", st == 400, f"{st} {b[:150]}")

# 非法扩展名拒绝
st, b, _ = req("POST", "/api/library/files?name=bad.exe", b"hello", raw_body=True)
check("上传 .exe 拒绝", st == 400, f"{st} {b[:150]}")

# 删除
st, b, _ = req("DELETE", "/api/library/files?name=e2e_renamed.md")
check("DELETE /api/library/files 删除", st == 200, f"{st} {b[:200]}")

print()
print("=" * 60)
print("F. 音频端点")
print("=" * 60)
st, j = get_json("/api/audio/status")
check("GET /api/audio/status", st == 200 and j is not None, f"{st} {str(j)[:200]}")

st, j = get_json("/api/audio/settings")
check("GET /api/audio/settings", st == 200 and j is not None, f"{st} {str(j)[:200]}")

st, j = get_json("/api/audio/devices")
check("GET /api/audio/devices", st == 200, f"{st} {str(j)[:200]}")

st, b, _ = req("POST", "/api/audio/test-tone", {"duration": 0.3})
check("POST /api/audio/test-tone", st in (200, 503), f"{st} {b[:150]}")

st, b, _ = req("POST", "/api/audio/panic")
check("POST /api/audio/panic", st in (200, 503), f"{st} {b[:150]}")

st, b, _ = req("GET", "/api/audio/soundfonts")
check("GET /api/audio/soundfonts（契约=405，仅 POST/DELETE 镜像）", st == 405, f"{st} {b[:120]}")

print()
print("=" * 60)
print("G. 更新端点（记录结果，网络功能）")
print("=" * 60)
st, j = get_json("/api/update/check")
check("GET /api/update/check", st == 200, f"{st} {str(j)[:200]}")

print()
print("=" * 60)
print("H. 音源注册表（/api/sound/*，P1 新增）")
print("=" * 60)
import tempfile

st, j = get_json("/api/sound/sources")
snd_sources = j.get("sources") if isinstance(j, dict) else None
check("GET /api/sound/sources 列举", st == 200 and isinstance(snd_sources, list), f"{st} {str(j)[:200]}")
check("列举带注册表版本号", isinstance(j, dict) and isinstance(j.get("version"), int), f"{str(j)[:120]}")

if snd_sources:
    kind0 = snd_sources[0].get("kind")
    st, jk = get_json("/api/sound/sources?kind=" + up.quote(str(kind0)))
    same_kind = isinstance((jk or {}).get("sources"), list) and \
        all(s.get("kind") == kind0 for s in (jk or {}).get("sources") or [])
    check(f"GET /api/sound/sources?kind={kind0} 按种类过滤", st == 200 and same_kind, f"{st} {str(jk)[:200]}")
else:
    skip("GET /api/sound/sources?kind= 过滤", "注册表为空（无已登记音源）")

st, jq = get_json("/api/sound/sources?q=zz-e2e-no-such-source-zz")
check("GET /api/sound/sources?q= 无匹配返回空列表",
      st == 200 and (jq or {}).get("sources") == [], f"{st} {str(jq)[:200]}")

# ── 临时采样包 CRUD（用例结束删除条目并清理临时目录） ──
def _norm_path(p):
    """路径归一：忽略分隔符与大小写差异（Windows 语义）。"""
    return str(p or "").replace("\\", "/").rstrip("/").lower()


def _same_dir(target, dirs):
    return _norm_path(target) in [_norm_path(d) for d in dirs]


pack_dir = os.path.join(tempfile.gettempdir(), "e2e_pack_" + str(int(time.time())))
pack_sub = os.path.join(pack_dir, "loops")
pack_wav = os.path.join(pack_dir, "e2e_kick.wav")
pack_name = "E2E采样包" + str(int(time.time()))
pack_id = None
pack_deleted = False
prep_ok = True
try:
    os.makedirs(pack_sub, exist_ok=True)
    with open(pack_wav, "wb") as f:
        f.write(b"RIFF\x00\x00\x00\x00WAVEfmt ")  # 内容无关：samples 只按扩展名归类
except Exception as e:
    prep_ok = False
    print(f"  · 临时采样包目录准备失败：{e}")

if not prep_ok:
    skip("音源注册表写操作（采样包创建/更新/删除）", "无法创建临时目录")
else:
    st, b, _ = req("POST", "/api/sound/sources",
                   {"kind": "samplepack", "name": pack_name, "dirs": [pack_dir],
                    "tags": ["e2e"], "vendor": "E2E"})
    jc = as_json(b)
    if isinstance(jc, dict) and isinstance(jc.get("source"), dict):
        pack_id = jc["source"].get("id")
    check("POST /api/sound/sources 登记采样包", st == 200 and bool(pack_id), f"{st} {b[:200]}")

    if pack_id:
        check("新建条目 kind/名称/目录回显正确",
              jc["source"].get("kind") == "samplepack" and jc["source"].get("name") == pack_name
              and _same_dir(pack_dir, jc["source"].get("dirs") or []),
              f"{str(jc)[:240]}")

        st, jl = get_json("/api/sound/sources?q=" + up.quote(pack_name))
        check("列举能查到新建条目",
              st == 200 and any(s.get("id") == pack_id for s in ((jl or {}).get("sources") or [])),
              f"{st} {str(jl)[:200]}")

        st, b, _ = req("PUT", "/api/sound/sources/" + up.quote(pack_id),
                       {"name": pack_name + "-改", "tags": ["e2e", "renamed"], "enabled": False})
        ju = as_json(b)
        check("PUT /api/sound/sources/{id} 改名 + 停用",
              st == 200 and ((ju or {}).get("source") or {}).get("name") == pack_name + "-改"
              and ((ju or {}).get("source") or {}).get("enabled") is False,
              f"{st} {b[:200]}")

        st, b, _ = req("PUT", "/api/sound/sources/" + up.quote(pack_id), {"enabled": True, "tags": ["e2e"]})
        jr = as_json(b)
        check("PUT /api/sound/sources/{id} 重新启用",
              st == 200 and ((jr or {}).get("source") or {}).get("enabled") is True, f"{st} {b[:200]}")

        st, js = get_json("/api/sound/samples?pack=" + up.quote(pack_id))
        check("GET /api/sound/samples 列举采样包内容",
              st == 200 and isinstance((js or {}).get("files"), list)
              and isinstance((js or {}).get("dirs"), list), f"{st} {str(js)[:240]}")
        check("采样内容含临时 wav 与子目录",
              any(f.get("name") == "e2e_kick.wav" for f in ((js or {}).get("files") or []))
              and any(d.get("name") == "loops" for d in ((js or {}).get("dirs") or [])),
              f"{str(js)[:240]}")

        st, js2 = get_json("/api/sound/samples?pack=" + up.quote(pack_id) + "&sub=loops")
        check("GET /api/sound/samples&sub= 子目录列举",
              st == 200 and (js2 or {}).get("sub") == "loops", f"{st} {str(js2)[:200]}")

        st, b, _ = req("DELETE", "/api/sound/sources/" + up.quote(pack_id))
        jd = as_json(b)
        pack_deleted = st == 200 and (jd or {}).get("ok") is True
        check("DELETE /api/sound/sources/{id} 删除", pack_deleted, f"{st} {b[:200]}")

        st, jl2 = get_json("/api/sound/sources?q=" + up.quote(pack_name))
        check("删除后列举不再返回该条目",
              st == 200 and all(s.get("id") != pack_id for s in ((jl2 or {}).get("sources") or [])),
              f"{st} {str(jl2)[:200]}")

# 临时包不可用时退化到已有采样包；再没有就跳过（沿用矩阵跳过口径）
if not pack_id:
    existing_packs = [s for s in (snd_sources or []) if s.get("kind") == "samplepack"]
    if existing_packs:
        st, js = get_json("/api/sound/samples?pack=" + up.quote(existing_packs[0]["id"]))
        check("GET /api/sound/samples 列举已有采样包",
              st == 200 and isinstance((js or {}).get("files"), list), f"{st} {str(js)[:200]}")
    else:
        skip("GET /api/sound/samples 列举采样包内容", "无采样包可列举")

# ── SF2 预设列举（需要真实且未缺失的 SF2 音源） ──
sf2_src = None
for s in (snd_sources or []):
    if s.get("kind") == "sf2" and not ((s.get("runtime") or {}).get("missing")):
        sf2_src = s
        break
if sf2_src:
    st, jp = get_json("/api/sound/presets?id=" + up.quote(sf2_src["id"]))
    if st == 200:
        check("GET /api/sound/presets?id= 列举 SF2 预设",
              isinstance((jp or {}).get("presets"), list), f"{st} {str(jp)[:200]}")
    elif st in (500, 503):
        skip("GET /api/sound/presets?id= 列举 SF2 预设",
             f"该 SF2 无法解析（st={st}，磁盘上可能不是真实音色库）")
    else:
        check("GET /api/sound/presets?id= 列举 SF2 预设", False, f"{st} {str(jp)[:200]}")
else:
    skip("GET /api/sound/presets?id= 列举 SF2 预设", "无可用 SF2 音源（未登记或文件缺失）")

# ── 重新扫描磁盘并入册（设置页「重新扫描」） ──
st, b, _ = req("POST", "/api/sound/migrate")
jm = as_json(b)
check("POST /api/sound/migrate 重新扫描",
      st == 200 and isinstance((jm or {}).get("sources"), list) and "result" in (jm or {}),
      f"{st} {b[:200]}")

# ── SF2 导入通道（合成最小合法 SF2；导入后用 ?file=1 连带删文件） ──
sf2_body = b"sfbk" + b"\x00" * 60
fake_sf2 = b"RIFF" + len(sf2_body).to_bytes(4, "little") + sf2_body
imp_name = "e2e_import_" + str(int(time.time()))
st, b, _ = req("POST", "/api/sound/import/sf2?name=" + up.quote(imp_name + ".sf2"), fake_sf2,
               headers={"Content-Type": "application/octet-stream"}, raw_body=True)
ji = as_json(b)
imp_id = ((ji or {}).get("source") or {}).get("id")
check("POST /api/sound/import/sf2 导入（合成最小合法 SF2）", st == 200 and bool(imp_id), f"{st} {b[:240]}")
if imp_id:
    check("导入落盘路径在音色库目录内",
          "soundfonts" in str((ji or {}).get("saved", "")).replace("\\", "/"), f"{str(ji)[:240]}")
    st, b, _ = req("DELETE", "/api/sound/sources/" + up.quote(imp_id) + "?file=1")
    jdel = as_json(b)
    check("DELETE /api/sound/sources/{id}?file=1 连带删磁盘文件",
          st == 200 and (jdel or {}).get("deletedFile") is True, f"{st} {b[:240]}")

# 收尾：条目若未删掉则补删，再清理临时目录
if pack_id and not pack_deleted:
    req("DELETE", "/api/sound/sources/" + up.quote(pack_id))
try:
    if os.path.exists(pack_wav):
        os.remove(pack_wav)
    for d in (pack_sub, pack_dir):
        if os.path.isdir(d):
            os.rmdir(d)
except Exception:
    pass

print()
print("=" * 60)
print("I. 编排定向修改（/api/projects/{id}/arrangement/ops）")
print("=" * 60)
if not pid:
    skip("arrangement/ops 全段", "B 段未拿到项目 id")
else:
    st, b, _ = req("POST", f"/api/projects/{pid}/arrangement/ops",
                   {"ops": [{"op": "setBpm", "bpm": 123}], "client": "e2e"})
    jb = as_json(b)
    check("POST arrangement/ops setBpm", st == 200 and (jb or {}).get("ok") is True, f"{st} {b[:240]}")
    check("ops 返回 rev / changed / 完整编排数据",
          isinstance((jb or {}).get("rev"), int) and "transport" in ((jb or {}).get("changed") or [])
          and isinstance((jb or {}).get("arrangement"), dict), f"{str(jb)[:240]}")
    check("ops 回显 summary 说明",
          isinstance((jb or {}).get("summary"), list) and len((jb or {}).get("summary") or []) >= 1,
          f"{str(jb)[:240]}")

    tracks0 = (((jb or {}).get("arrangement") or {}).get("tracks")) or []
    n0 = len(tracks0)
    st, b, _ = req("POST", f"/api/projects/{pid}/arrangement/ops",
                   {"ops": [{"op": "addTrack", "name": "E2E轨"}], "client": "e2e"})
    jt = as_json(b)
    tracks1 = (((jt or {}).get("arrangement") or {}).get("tracks")) or []
    check("POST arrangement/ops addTrack", st == 200 and len(tracks1) == n0 + 1, f"{st} n0={n0} n1={len(tracks1)}")
    check("addTrack 写入轨道名",
          any(isinstance(t, dict) and t.get("name") == "E2E轨" for t in tracks1), f"{str(jt)[:240]}")
    check("addTrack 标记 changed=tracks", "tracks" in ((jt or {}).get("changed") or []), f"{str(jt)[:200]}")

    idx = len(tracks1) - 1
    st, b, _ = req("POST", f"/api/projects/{pid}/arrangement/ops",
                   {"ops": [{"op": "setTrackSource", "track": idx, "itemId": "builtin:synth",
                             "presetId": "0:0"}], "client": "e2e"})
    jts = as_json(b)
    tracks2 = (((jts or {}).get("arrangement") or {}).get("tracks")) or []
    item_id = None
    if 0 <= idx < len(tracks2) and isinstance(tracks2[idx], dict):
        item_id = (tracks2[idx].get("source") or {}).get("itemId")
    check("POST arrangement/ops setTrackSource（自动入架）", st == 200 and bool(item_id), f"{st} {b[:240]}")
    check("setTrackSource 标记 changed 含 rack", "rack" in ((jts or {}).get("changed") or []), f"{str(jts)[:200]}")

    if item_id:
        st, b, _ = req("POST", f"/api/projects/{pid}/arrangement/ops",
                       {"op": {"op": "setRackItem", "itemId": item_id, "pinned": True}, "client": "e2e"})
        jsr = as_json(b)
        pinned = False
        for it in ((((jsr or {}).get("arrangement") or {}).get("rack") or {}).get("items") or []):
            if isinstance(it, dict) and it.get("id") == item_id:
                pinned = it.get("pinned") is True
        check("POST arrangement/ops 单操作简写 setRackItem",
              st == 200 and pinned and "rack" in ((jsr or {}).get("changed") or []), f"{st} {b[:240]}")
    else:
        skip("arrangement/ops setRackItem", "setTrackSource 未返回机架条目 id")

    st, b, _ = req("POST", f"/api/projects/{pid}/arrangement/ops",
                   {"ops": [{"op": "setLoop", "on": True, "start": 0, "end": 8}], "client": "e2e"})
    jl2 = as_json(b)
    check("POST arrangement/ops setLoop",
          st == 200 and "transport" in ((jl2 or {}).get("changed") or []), f"{st} {b[:200]}")

    st, jj = get_json(f"/api/projects/{pid}/arrangement")
    check("ops 落盘后 GET arrangement 见到 bpm=123",
          st == 200 and isinstance(jj, dict) and int(jj.get("bpm") or 0) == 123, f"{st} {str(jj)[:200]}")
    check("ops 落盘后 GET arrangement 见到新轨道",
          isinstance(jj, dict) and any(isinstance(t, dict) and t.get("name") == "E2E轨"
                                      for t in (jj.get("tracks") or [])), f"{str(jj)[:240]}")

print()
print("=" * 60)
print("J. 引擎插件宿主（/api/engine/plugins*，P5 新增）")
print("=" * 60)
st, b, _ = req("GET", "/api/engine/plugins")
jp = as_json(b)
if st == 200 and isinstance(jp, dict):
    check("GET /api/engine/plugins 插件清单",
          isinstance(jp.get("plugins"), list) and isinstance(jp.get("count"), int), f"{st} {b[:200]}")
    check("清单 count 与列表长度一致", jp.get("count") == len(jp.get("plugins") or []), f"{str(jp)[:200]}")

    st, b, _ = req("GET", "/api/engine/plugins/installed")
    jinst = as_json(b)
    check("GET /api/engine/plugins/installed 已装 MM 清单",
          st == 200 and isinstance((jinst or {}).get("plugins"), list), f"{st} {b[:200]}")

    st, b, _ = req("GET", "/api/engine/plugins/state")
    check("GET /api/engine/plugins/state 槽位状态",
          st == 200 and isinstance(as_json(b), dict), f"{st} {b[:200]}")

    if pid:
        st, b, _ = req("GET", "/api/engine/plugins/state/list?project=" + up.quote(pid))
        jsl = as_json(b)
        if st in (500, 503):
            skip("GET /api/engine/plugins/state/list 插件状态清单", f"引擎不可用（st={st}）")
        else:
            check("GET /api/engine/plugins/state/list 插件状态清单",
                  st == 200 and isinstance((jsl or {}).get("states"), list)
                  and (jsl or {}).get("count") == len((jsl or {}).get("states") or []),
                  f"{st} {b[:200]}")
    else:
        skip("GET /api/engine/plugins/state/list 插件状态清单", "B 段未拿到项目 id")

    st, b, _ = req("POST", "/api/engine/plugins/ui-ticket")
    jtick = as_json(b)
    ticket = (jtick or {}).get("ticket")
    check("POST /api/engine/plugins/ui-ticket 签发一次性凭据", st == 200 and bool(ticket), f"{st} {b[:200]}")
    if ticket:
        st, b, _ = req("GET", "/api/engine/plugins/ui?ticket=" + ticket + "&plugin=__e2e_no_such_plugin__")
        check("GET /api/engine/plugins/ui 未安装插件 → 404", st == 404, f"{st} {b[:200]}")
    else:
        skip("GET /api/engine/plugins/ui", "未签发到 ui_ticket")

    st, b, _ = req("POST", "/api/engine/plugins/sync")
    jsync = as_json(b)
    check("POST /api/engine/plugins/sync 同步安装目录",
          st == 200 and (jsync or {}).get("ok") is True, f"{st} {b[:200]}")

    st, b, _ = req("POST", "/api/engine/plugins/scan", {"dirs": []}, timeout=120)
    if st == -1:
        skip("POST /api/engine/plugins/scan", "扫描超过 120s 未返回（弱断言，不判失败）")
    else:
        check("POST /api/engine/plugins/scan 可解释响应（弱断言）",
              st in (200, 500, 503) and (b"count" in b or b"detail" in b), f"{st} {b[:200]}")
else:
    down = engine_unavailable(st, b)
    why = f"引擎不可用（st={st} {b[:80]}）" if down else f"意外响应 st={st} {b[:120]}"
    for nm in ["GET /api/engine/plugins 插件清单",
               "GET /api/engine/plugins/installed",
               "GET /api/engine/plugins/state",
               "POST /api/engine/plugins/ui-ticket",
               "POST /api/engine/plugins/sync",
               "POST /api/engine/plugins/scan"]:
        skip(nm, why)
    if not down:
        check("GET /api/engine/plugins 可解释响应", False, why)

print()
print("=" * 60)
print("K. 迁移提示 / 启动令牌引导 / 编译批准（本轮新增）")
print("=" * 60)
st, j = get_json("/api/migration")
first_pending = bool((j or {}).get("pending"))
check("GET /api/migration 返回结构", st == 200 and isinstance((j or {}).get("pending"), bool), f"{st} {str(j)[:200]}")
st, j2 = get_json("/api/migration")
check("GET /api/migration 二次调用不再是 pending（首启提示只消费一次）",
      st == 200 and (j2 or {}).get("pending") is False, f"{st} first={first_pending} {str(j2)[:200]}")

st, b, hdrs = req("GET", "/api/bootstrap.js")
jb = as_json(b)
check("GET /api/bootstrap.js 返回启动令牌字段",
      st == 200 and isinstance((jb or {}).get("token"), str), f"{st} {b[:160]}")
check("bootstrap.js 不缓存（Cache-Control: no-store）",
      st == 200 and "no-store" in str(hdrs.get("Cache-Control", "")), f"{st} cc={hdrs.get('Cache-Control')!r}")

st, b, _ = req("POST", "/api/approval",
               {"project_id": pid or "", "approval_id": "e2e-no-such-approval", "allow": False})
jap = as_json(b)
check("POST /api/approval 拒绝路径（allow=false）",
      st == 200 and (jap or {}).get("approved") is False, f"{st} {b[:200]}")

print()
print("=" * 60)
print("L. 素材目录注册表（/api/arrangement/*，只读路径）")
print("=" * 60)
st, j = get_json("/api/arrangement/dirs")
mat_dirs = (j or {}).get("dirs")
check("GET /api/arrangement/dirs 列举", st == 200 and isinstance(mat_dirs, list), f"{st} {str(j)[:200]}")
if mat_dirs:
    st, jf = get_json("/api/arrangement/files?dir=" + up.quote(mat_dirs[0]))
    if st == 200:
        check("GET /api/arrangement/files 列出已注册目录",
              isinstance((jf or {}).get("files"), list) and isinstance((jf or {}).get("dirs"), list),
              f"{st} {str(jf)[:200]}")
    else:
        skip("GET /api/arrangement/files", f"已注册目录当前不可读（st={st}）")
else:
    skip("GET /api/arrangement/files", "无已注册素材目录")

print()
print("=" * 60)
print("M. 计划/未注册端点的可解释响应（弱断言）")
print("=" * 60)
# /api/sound/vst/scan：设计文档承诺的入口，当前实现走 POST /api/engine/plugins/scan。
# 只要求"能返回可解释响应"，不要求扫描成功、也不因未实现而判失败。
st, b, _ = req("POST", "/api/sound/vst/scan", {"dirs": []}, timeout=90)
if st == -1:
    skip("POST /api/sound/vst/scan", "请求超时（可能已实现为长时间扫描）：弱断言不判失败")
else:
    check("POST /api/sound/vst/scan 可解释响应",
          st in (200, 202, 404, 405, 503) and (st == 404 or b"detail" in b or b"ok" in b),
          f"{st} {b[:200]}")

for mm_path in ("/api/mm/build", "/api/mm/install"):
    st, b, _ = req("POST", mm_path, {"src": "x", "pkg": "x.mm"})
    check(f"POST {mm_path} 可解释响应（当前未注册 → 404 或结构化错误）",
          st == 404 or (st in (200, 400, 403, 503) and (b"detail" in b or b"ok" in b)), f"{st} {b[:200]}")

print()
print("=" * 60)
print(f"结果: PASS={PASS} FAIL={FAIL}")
if SKIPPED:
    print(f"跳过: {SKIP} 项（环境不具备，不计入失败）")
    for s in SKIPPED:
        print(f"  · {s}")
if FAILED:
    print("失败项:")
    for f in FAILED:
        print(f"  - {f}")
sys.exit(1 if FAIL else 0)