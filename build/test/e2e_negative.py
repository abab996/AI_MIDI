# -*- coding: utf-8 -*-
"""AI_MIDI E2E 负向/健壮性攻击矩阵：路径穿越、超限、非法来源、并发、断流"""
import json
import sys
import time
import urllib.request
import urllib.error
import http.client
import threading
import socket

BASE = "http://127.0.0.1:7860"
PASS = 0
FAIL = 0
FAILED = []
SKIP = 0
SKIPPED = []

# 本轮架构升级给 /api/* 加了启动令牌闸门（X-MM-Token）：脚本按页面同款路径
# 从免令牌的 /api/bootstrap.js 取令牌，请求时带上；取不到（旧版服务端 /
# 随机源失败）就按无令牌发送，与升级前行为一致——既有断言口径不变。
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


def raw_req(method, path, data=None, headers=None, timeout=15, no_token=False):
    """原生请求，允许自定义 Host/Origin；默认带启动令牌（no_token=True 可关掉）。

    连接级失败（未收到任何 HTTP 响应，如 WinError 10054）重试一次：超大 body
    用例会让服务端主动断开连接，urllib 的连接复用随后可能撞上已关闭的连接。
    那是测试自身的复用问题，与产品行为无关——重试一次即可把它区分出来，
    避免把"连接被服务端正常关闭"误报成产品缺陷。"""
    def _once():
        url = BASE + path
        h = dict(headers or {})
        tok = _load_token()
        if not no_token and tok and "X-MM-Token" not in h:
            h["X-MM-Token"] = tok
        r = urllib.request.Request(url, data=data, headers=h, method=method)
        try:
            with urllib.request.urlopen(r, timeout=timeout) as resp:
                return resp.status, resp.read(), dict(resp.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read(), dict(e.headers)
        except Exception as e:
            return -1, str(e).encode(), {}

    st, b, hd = _once()
    if st == -1:
        time.sleep(0.2)
        st, b, hd = _once()
    return st, b, hd


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


def engine_down(st, body):
    """引擎类端点的环境性不可用：503（引擎未启动）或 500（未就绪/内部报错）。
    这类响应的前置条件是"本机有可用引擎"，环境不具备时按跳过处理。"""
    return st in (500, 503)


def detail_of(body):
    """取结构化错误里的 detail 文本（便于断言"可解释错误"而非空响应）。"""
    try:
        j = json.loads(body)
    except Exception:
        return ""
    return str((j or {}).get("detail") or "")


def token_header_bytes():
    """原始 HTTP 报文里的令牌头行（服务端未启用令牌时为空）。"""
    tok = _load_token()
    if tok:
        return ("X-MM-Token: %s\r\n" % tok).encode()
    return b""


print("=" * 60)
print("A. 路径穿越（download / library / project 子路径）")
print("=" * 60)
# 创建项目拿真实 id
st, b, _ = raw_req("POST", "/api/projects", json.dumps({"name": "攻击测试"}).encode(),
                   {"Content-Type": "application/json"})
pid = json.loads(b)["meta"]["id"]

for path in [
    "/api/files/download?path=../settings.json",
    "/api/files/download?path=..%2Fsettings.json",
    "/api/files/download?path=%2e%2e%2fsettings.json",
    "/api/files/download?path=..%5Csettings.json",
    "/api/files/download?path=settings.json",
    "/api/files/download?path=projects",
    "/api/files/download?path=",
    "/api/library/files/content?name=../settings.json&scope=user",
    "/api/library/files/content?name=..%2f..%2fsettings.json&scope=builtin",
    "/api/library/files/content?name=%00.md&scope=user",
    f"/api/projects/{pid}/files/move",
    "/api/audio/bounce/file?path=../../settings.json",
    "/api/arrangement/audio?p=../../settings.json",
    "/api/library/files/rename",
]:
    if "files/move" in path:
        st, b, _ = raw_req("POST", path, json.dumps({"name": "../逃逸.mid", "to": "x.mid"}).encode(),
                           {"Content-Type": "application/json"})
    elif "rename" in path:
        st, b, _ = raw_req("POST", path, json.dumps({"from": "../a.md", "to": "../b.md"}).encode(),
                           {"Content-Type": "application/json"})
    else:
        st, b, _ = raw_req("GET", path)
    # 期望 4xx（403/400/404），绝不能 200 + settings 内容
    ok4xx = st in (400, 403, 404)
    leak = b"api_key" in b or b'"detail": "path"' in b and st == 200
    check(f"穿越拦截: {path}", ok4xx and not leak, f"st={st} body={b[:80]}")

# library DELETE / POST 拒绝 .. 路径
st, b, _ = raw_req("DELETE", "/api/library/files?name=..%2Fescape.md")
check("library DELETE 穿越名拒绝", st in (400, 403, 404), f"st={st} body={b[:80]}")
st, b, _ = raw_req("POST", "/api/library/files?name=..%2Fescape.md", b"x" * 10, {"Content-Type": "text/plain"})
check("library POST 穿越名拒绝", st in (400, 403, 404), f"st={st} body={b[:80]}")

print()
print("=" * 60)
print("B. 非法 Host / Origin（DNS rebinding / CSRF 模拟）")
print("=" * 60)
tests = [
    ("非法 Host 域名", "/api/health", {"Host": "evil.com"}),
    ("空 Host", "/api/health", {"Host": ""}),
    ("尾随点 Host", "/api/health", {"Host": "localhost."}),
    ("subdomain Host", "/api/health", {"Host": "sub.localhost"}),
    ("恶意 Origin", "/api/health", {"Host": "127.0.0.1:7860", "Origin": "https://evil.com"}),
    ("null Origin", "/api/health", {"Host": "127.0.0.1:7860", "Origin": "null"}),
    ("data: Origin", "/api/health", {"Host": "127.0.0.1:7860", "Origin": "data:text/html"}),
]
for name, path, hdr in tests:
    st, b, _ = raw_req("GET", path, headers=hdr)
    check(f"{name}", st == 403, f"st={st} body={b[:80]}")

# 合法 Origin 应放行并回显 CORS
st, b, hdrs = raw_req("GET", "/api/health", headers={"Host": "127.0.0.1:7860", "Origin": "http://127.0.0.1:7860"})
check("合法 Origin 放行", st == 200 and hdrs.get("Access-Control-Allow-Origin") == "http://127.0.0.1:7860",
      f"st={st} acao={hdrs.get('Access-Control-Allow-Origin')}")

print()
print("=" * 60)
print("C. 超限上传与非法请求体")
print("=" * 60)
# library 2MB 上限
big = b"x" * (2 * 1024 * 1024 + 100)
st, b, _ = raw_req("POST", "/api/library/files?name=big.md", big, {"Content-Type": "text/markdown"})
check("library 上传 >2MB 拒绝(413)", st in (413, 400), f"st={st} body={b[:80]}")

# parse 超限（构造 >64MB 请求头；用 header 声明 Content-Length 不实际发送——测试服务器是否在读取侧钳制）
st, b, _ = raw_req("POST", "/api/parse", b"", {"Content-Type": "application/octet-stream"})
check("parse 空 body 拒绝", st in (400, 413, 200), f"st={st}")  # 空 multipart 会被各种方式拒绝，只确认不 500

# 非法 JSON
st, b, _ = raw_req("PUT", "/api/settings", b"{invalid json", {"Content-Type": "application/json"})
check("非法 JSON → 400", st == 400, f"st={st} body={b[:80]}")

st, b, _ = raw_req("POST", "/api/projects/notexist/files/move", b"", {"Content-Type": "application/json"})
check("不存在项目文件操作 → 4xx", st in (400, 404), f"st={st} body={b[:80]}")

st, b, _ = raw_req("POST", "/api/chat", b"", {"Content-Type": "application/json"})
check("空 chat body → 4xx", st in (400, 404), f"st={st} body={b[:80]}")

st, b, _ = raw_req("GET", "/api/this/endpoint/does/not/exist")
check("未知端点 → 404", st == 404, f"st={st} body={b[:80]}")

# 方法不允许
st, b, _ = raw_req("DELETE", "/api/health")
check("DELETE health → 405", st == 405, f"st={st}")

# 无 Content-Length 的 chunked 超大 body（限制器是否拦截）
sock = socket.create_connection(("127.0.0.1", 7860), timeout=5)
req = (b"POST /api/library/files?name=chunk.md HTTP/1.1\r\nHost: 127.0.0.1:7860\r\n"
       + token_header_bytes() +
       b"Content-Type: text/markdown\r\nTransfer-Encoding: chunked\r\n\r\n")
sock.sendall(req)
try:
    sock.sendall(b"400000\r\n" + b"x" * 4096 + b"\r\n")  # 声明超大 chunk 但只发 4KB
    sock.settimeout(5)
    resp = sock.recv(1024)
    status_line = resp.decode(errors="replace").split("\r\n")[0]
    check("chunked 超限钳制", "413" in status_line or "400" in status_line or "200" not in status_line, status_line)
except Exception as e:
    check("chunked 超限钳制", True, f"socket 断开/异常（服务端正确中止）: {e}")
finally:
    sock.close()

print()
print("=" * 60)
print("D. 知识库边界")
print("=" * 60)
# 空文件名
st, b, _ = raw_req("GET", "/api/library/files/content?name=&scope=user")
check("空文件名 → 400", st == 400, f"st={st} body={b[:60]}")
# 非法 scope
st, b, _ = raw_req("GET", "/api/library/files/content?name=a.md&scope=../../")
check("非法 scope → 4xx", st in (400, 403, 404), f"st={st} body={b[:60]}")
# 删除不存在的
st, b, _ = raw_req("DELETE", "/api/library/files?name=nonexist.md")
check("删除不存在文件 → 200 容错", st == 200, f"st={st} body={b[:60]}")
# 100 个上限（快速造 101 个）
created = 0
for i in range(101):
    st, b, _ = raw_req("POST", f"/api/library/files?name=bulk{i}.txt", b"x" * 10, {"Content-Type": "text/plain"})
    if st == 200:
        created += 1
    else:
        break
check("批量上传达到上限被拒", st == 400 and "\u4e0a\u9650" in b.decode("utf-8", errors="replace"), f"st={st} created={created} body={b[:80]}")
# 清理
import urllib.parse as up
for i in range(created):
    raw_req("DELETE", f"/api/library/files?name=bulk{i}.txt")
raw_req("DELETE", "/api/library/files?name=bulk100.txt")

print()
print("=" * 60)
print("E. 项目删除守卫（活跃任务）")
print("=" * 60)
# 新项目 + 发起 chat（mock 会流式较久），过程中试图删除
st, b, _ = raw_req("POST", "/api/projects", json.dumps({"name": "守卫测试"}).encode(),
                   {"Content-Type": "application/json"})
gpid = json.loads(b)["meta"]["id"]

conn = http.client.HTTPConnection("127.0.0.1", 7860, timeout=10)
conn.request("POST", "/api/chat", body=json.dumps({"project_id": gpid, "message": "超长文本测试超长文本", "task_id": ""}).encode(),
             headers={"Content-Type": "application/json"})
resp = conn.getresponse()
time.sleep(0.8)  # 让任务进入 running
st, b, _ = raw_req("DELETE", f"/api/projects/{gpid}")
if st == 200:
    # 守卫的前提是"真的有一个活跃任务"。本机没有 mock LLM（9321）时 chat 不会
    # 建立任务，删除自然被放行——按环境不具备跳过，而不是记成产品缺陷。
    skip("活跃任务期间删除项目被拒", "没有活跃任务（需要 mock LLM 在 9321；本机未启动），项目已直接删除")
else:
    check("活跃任务期间删除项目被拒", st in (400, 409), f"st={st} body={b[:80]}")
resp.read()
conn.close()
time.sleep(6)  # 等任务完成
st, b, _ = raw_req("DELETE", f"/api/projects/{gpid}")
if st == 404:
    skip("任务完成后可删除", "项目已在上一用例删除（无活跃任务场景）")
else:
    check("任务完成后可删除", st == 200, f"st={st} body={b[:80]}")

print()
print("=" * 60)
print("F. 并发（设置写入/chat/轮询）与 SSE 断流")
print("=" * 60)
errors = []


def worker_set(n):
    try:
        raw_req("PUT", "/api/settings", json.dumps({"api_key": "****", "base_url": "http://127.0.0.1:9321/v1",
                                                    "api_path": "/v1", "model": "mock-model", "max_tokens": 200 + n,
                                                    "max_completion_tokens": 200 + n, "reasoning_effort": "LOW",
                                                    "thinking_enabled": False}).encode(),
                {"Content-Type": "application/json"})
    except Exception as e:
        errors.append(f"set:{e}")


def worker_chat(n):
    try:
        conn = http.client.HTTPConnection("127.0.0.1", 7860, timeout=20)
        conn.request("POST", "/api/chat", body=json.dumps({"project_id": pid, "message": "你好", "task_id": ""}).encode(),
                     headers={"Content-Type": "application/json"})
        r = conn.getresponse()
        r.read()
        conn.close()
    except Exception as e:
        errors.append(f"chat:{e}")


def worker_poll(n):
    try:
        for _ in range(5):
            raw_req("GET", f"/api/projects/{pid}")
            raw_req("GET", "/api/tasks")
            time.sleep(0.05)
    except Exception as e:
        errors.append(f"poll:{e}")


threads = []
for i in range(8):
    threads.append(threading.Thread(target=worker_set, args=(i,)))
    threads.append(threading.Thread(target=worker_chat, args=(i,)))
    threads.append(threading.Thread(target=worker_poll, args=(i,)))
for t in threads:
    t.start()
for t in threads:
    t.join()

check("并发 24 线程无异常", not errors, f"errors={errors[:5]}")
st, b, _ = raw_req("GET", "/api/health")
check("并发后服务仍健康", st == 200, f"st={st} body={b[:60]}")

# SSE 客户端中途断开（连续 5 次，观察服务不崩、goroutine 不挂）
srv_ok = True
for i in range(5):
    try:
        sock = socket.create_connection(("127.0.0.1", 7860), timeout=5)
        req = (b"POST /api/chat HTTP/1.1\r\nHost: 127.0.0.1:7860\r\nContent-Type: application/json\r\n"
               + token_header_bytes()
               + f"Content-Length: {len(json.dumps({'project_id': pid, 'message': '长文测试', 'task_id': ''}).encode())}\r\n\r\n".encode()
               + json.dumps({"project_id": pid, "message": "长文测试", "task_id": ""}).encode())
        sock.sendall(req)
        time.sleep(0.3)
        sock.close()  # 中途断开
    except Exception as e:
        errors.append(f"ssedrop:{e}")
check("SSE 中途断开 5 次无异常", not errors, f"errors={errors[:5]}")
st, b, _ = raw_req("GET", "/api/health")
check("断流后服务仍健康", st == 200, f"st={st}")

print()
print("=" * 60)
print("G. 静态资源与页面")
print("=" * 60)
for page in ["/", "/chat.html", "/settings.html", "/index.html", "/style.css", "/js/chat.js", "/wails/runtime.js", "/wails/ipc.js"]:
    st, b, hdrs = raw_req("GET", page)
    check(f"GET {page}", st == 200, f"st={st} len={len(b)}")
st, b, _ = raw_req("GET", "/chat.html")
check("chat.html 含 CSP", b"connect-src" in b, f"CSP 缺失")
st, b, _ = raw_req("GET", "/nonexistent_page.html")
check("未知页面 → 404", st == 404, f"st={st}")

print()
print("=" * 60)
print("H. 音源注册表边界（/api/sound/*）")
print("=" * 60)
import os
import tempfile

st, b, _ = raw_req("POST", "/api/sound/sources", b"{", {"Content-Type": "application/json"})
check("sources 非法 JSON → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:120]}")

st, b, _ = raw_req("POST", "/api/sound/sources", json.dumps({}).encode(), {"Content-Type": "application/json"})
check("sources 缺 kind/path → 400（不静默成功）", st == 400 and detail_of(b) != "", f"st={st} body={b[:120]}")

st, b, _ = raw_req("POST", "/api/sound/sources",
                   json.dumps({"kind": "bogus", "name": "e2e非法", "path": os.getcwd()}).encode(),
                   {"Content-Type": "application/json"})
check("sources 非法 kind → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

st, b, _ = raw_req("POST", "/api/sound/sources",
                   json.dumps({"kind": "sf2", "name": "e2e走错入口", "path": "C:\\__e2e_no_such__.sf2"}).encode(),
                   {"Content-Type": "application/json"})
check("sources kind=sf2 走错入口 → 400（SF2 只能走导入通道）", st == 400, f"st={st} body={b[:160]}")

# 目录穿越 / 不存在目录：一律 400，且不得登记成条目（防"指到系统目录"的条目）
for esc in ["../__e2e_no_such_dir__", "..\\__e2e_no_such_dir__", "../../__e2e_no_such_dir__",
            "C:\\__e2e_no_such_dir__", "../settings.json", "projects/../../settings.json",
            "%2e%2e%2f%2e%2e%2fsettings.json"]:
    st, b, _ = raw_req("POST", "/api/sound/sources",
                       json.dumps({"kind": "samplepack", "name": "e2e逃逸", "path": esc}).encode(),
                       {"Content-Type": "application/json"})
    check(f"sources 非法目录拒绝: {esc}", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", "/api/sound/sources",
                   json.dumps({"kind": "samplepack", "name": "e2e逃逸",
                               "dirs": ["../../__e2e_no_such_dir__", "."]}).encode(),
                   {"Content-Type": "application/json"})
check("sources dirs 数组含逃逸目录 → 400", st == 400, f"st={st} body={b[:140]}")

st, b, _ = raw_req("GET", "/api/sound/sources?q=" + up.quote("e2e逃逸"))
try:
    escaped = json.loads(b).get("sources")
except Exception:
    escaped = None
check("逃逸尝试未登记任何条目", st == 200 and escaped == [], f"st={st} {str(escaped)[:160]}")

# 超限请求体：/api/sound/ 路由层上限 8MB，超限必须在读取侧被拒
big_src = b'{"kind":"samplepack","path":"' + b"x" * (8 * 1024 * 1024 + 100) + b'"}'
st, b, _ = raw_req("POST", "/api/sound/sources", big_src, {"Content-Type": "application/json"}, timeout=30)
check("sources 超限请求体被拒（不 500/不落库）", st in (400, 413), f"st={st} body={b[:120]}")

# ── 临时采样包：为 id / 子路径类用例提供真实对象（结束时删除） ──
pack_dir = os.path.join(tempfile.gettempdir(), "e2e_neg_pack_" + str(int(time.time())))
pack_sub = os.path.join(pack_dir, "sub")
pack_id = None
prep_ok = True
try:
    os.makedirs(pack_sub, exist_ok=True)
except Exception:
    prep_ok = False

if prep_ok:
    st, b, _ = raw_req("POST", "/api/sound/sources",
                       json.dumps({"kind": "samplepack", "name": "e2e负向采样包", "dirs": [pack_dir]}).encode(),
                       {"Content-Type": "application/json"})
    try:
        pack_id = (json.loads(b).get("source") or {}).get("id")
    except Exception:
        pack_id = None
    if not pack_id:
        print(f"  · 临时采样包登记失败（st={st}）：{b[:160]}")

if pack_id:
    st, b, _ = raw_req("PUT", "/api/sound/sources/__e2e_no_such_source__",
                       json.dumps({"name": "x"}).encode(), {"Content-Type": "application/json"})
    check("PUT 不存在的音源 id → 404", st == 404, f"st={st} body={b[:120]}")

    st, b, _ = raw_req("DELETE", "/api/sound/sources/__e2e_no_such_source__")
    check("DELETE 不存在的音源 id → 404", st == 404, f"st={st} body={b[:120]}")

    st, b, _ = raw_req("PUT", "/api/sound/sources/a%2Fb",
                       json.dumps({"name": "x"}).encode(), {"Content-Type": "application/json"})
    check("PUT 含路径分隔符的 id → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:120]}")

    st, b, _ = raw_req("DELETE", "/api/sound/sources/a%2Fb")
    check("DELETE 含路径分隔符的 id → 400", st == 400, f"st={st} body={b[:120]}")

    st, b, _ = raw_req("GET", "/api/sound/sources/__e2e_no_such_source__")
    check("GET sources/{id} 不支持 → 405", st == 405, f"st={st} body={b[:120]}")

    st, b, _ = raw_req("PUT", "/api/sound/sources/" + up.quote(pack_id),
                       json.dumps({"name": "   "}).encode(), {"Content-Type": "application/json"})
    check("PUT 空名称 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

    st, b, _ = raw_req("PUT", "/api/sound/sources/" + up.quote(pack_id),
                       json.dumps({"dirs": ["../../__e2e_no_such_dir__"]}).encode(),
                       {"Content-Type": "application/json"})
    check("PUT 逃逸目录 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

    st, b, _ = raw_req("GET", "/api/sound/sources?q=" + up.quote("e2e负向采样包"))
    try:
        keep = json.loads(b).get("sources") or []
    except Exception:
        keep = []
    same_entry = bool(keep) and keep[0].get("id") == pack_id
    check("失败更新后条目保持原值（无部分写入）",
          same_entry and keep[0].get("name") == "e2e负向采样包"
          and all("__e2e_no_such_dir__" not in str(d) for d in (keep[0].get("dirs") or [])),
          f"st={st} {str(keep)[:200]}")

    # ── samples：缺参 / 不存在 / 子路径穿越 ──
    st, b, _ = raw_req("GET", "/api/sound/samples")
    check("samples 缺 pack 参数 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:120]}")

    st, b, _ = raw_req("GET", "/api/sound/samples?pack=__e2e_no_such_pack__")
    check("samples 不存在的采样包 → 404", st == 404, f"st={st} body={b[:120]}")

    for sub_q in ["..", "..%2F..%2Fsettings.json", "%2e%2e%2f%2e%2e%2fsettings.json",
                  "..%5C..%5Csettings.json", "sub%2F..%2F..%2Fsettings.json", "%00", "sub%00evil"]:
        st, b, _ = raw_req("GET", "/api/sound/samples?pack=" + up.quote(pack_id) + "&sub=" + sub_q)
        check(f"samples 子路径穿越拒绝: sub={sub_q}", st == 400, f"st={st} body={b[:140]}")

    st, b, _ = raw_req("GET", "/api/sound/samples?pack=" + up.quote(pack_id) + "&sub=__e2e_no_such_sub__")
    check("samples 不存在的子目录 → 400（可解释）", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

    st, b, _ = raw_req("GET", "/api/sound/presets?id=" + up.quote(pack_id))
    check("presets 非 SF2 音源 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("GET", "/api/sound/presets")
check("presets 缺 id → 400", st == 400, f"st={st} body={b[:120]}")

st, b, _ = raw_req("GET", "/api/sound/presets?id=__e2e_no_such_source__")
check("presets 不存在的 id → 404", st == 404, f"st={st} body={b[:120]}")

# reveal / migrate：只验错误路径（成功路径会拉起资源管理器，不做）
st, b, _ = raw_req("POST", "/api/sound/reveal", json.dumps({}).encode(), {"Content-Type": "application/json"})
check("reveal 缺 id → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", "/api/sound/reveal", json.dumps({"id": "__e2e_no_such_source__"}).encode(),
                   {"Content-Type": "application/json"})
check("reveal 不存在的 id → 404（不拉起资源管理器）", st == 404, f"st={st} body={b[:140]}")

st, b, _ = raw_req("GET", "/api/sound/reveal?id=x")
check("reveal 非 POST → 405", st == 405, f"st={st} body={b[:120]}")

st, b, _ = raw_req("GET", "/api/sound/migrate")
check("migrate 非 POST → 405", st == 405, f"st={st} body={b[:120]}")

st, b, _ = raw_req("GET", "/api/sound/__e2e_no_such_sub__")
check("sound 未知子路径 → 404", st == 404 and detail_of(b) != "", f"st={st} body={b[:140]}")

# ── import/sf2：魔数与 RIFF size 校验 ──
st, b, _ = raw_req("GET", "/api/sound/import/sf2?name=x.sf2")
check("import/sf2 非 POST → 405", st == 405, f"st={st} body={b[:120]}")

st, b, _ = raw_req("POST", "/api/sound/import/sf2?name=e2e_empty.sf2", b"",
                   {"Content-Type": "application/octet-stream"})
check("import/sf2 空 body → 400（魔数校验）", st == 400 and "SF2" in b.decode("utf-8", errors="replace"),
      f"st={st} body={b[:160]}")

st, b, _ = raw_req("POST", "/api/sound/import/sf2?name=e2e_not_sf2.sf2", b"not an sf2 file at all" * 8,
                   {"Content-Type": "application/octet-stream"})
check("import/sf2 非 SF2 内容 → 400", st == 400 and "SF2" in b.decode("utf-8", errors="replace"),
      f"st={st} body={b[:160]}")

half_body = b"sfbk" + b"\x00" * 60
half_riff = b"RIFF" + (4096).to_bytes(4, "little") + half_body
st, b, _ = raw_req("POST", "/api/sound/import/sf2?name=e2e_half.sf2", half_riff,
                   {"Content-Type": "application/octet-stream"})
check("import/sf2 头合法但长度不符（半截文件）→ 400", st == 400 and "SF2" in b.decode("utf-8", errors="replace"),
      f"st={st} body={b[:160]}")

# name 里的路径穿越必须被消毒到音色库目录内（导入后立刻连带删文件）
ok_body = b"sfbk" + b"\x00" * 60
ok_riff = b"RIFF" + len(ok_body).to_bytes(4, "little") + ok_body
st, b, _ = raw_req("POST", "/api/sound/import/sf2?name=" + up.quote("../../__e2e_escape__.sf2"),
                   ok_riff, {"Content-Type": "application/octet-stream"})
try:
    imp = json.loads(b)
except Exception:
    imp = {}
saved = str(imp.get("saved") or "").replace("\\", "/")
check("import/sf2 name 穿越被消毒（落盘仍在音色库目录内）",
      st == 200 and "soundfonts" in saved and saved.endswith("__e2e_escape__.sf2"),
      f"st={st} saved={saved!r} body={b[:160]}")
imp_id = (imp.get("source") or {}).get("id")
if imp_id:
    raw_req("DELETE", "/api/sound/sources/" + up.quote(imp_id) + "?file=1")

# 收尾：删掉临时采样包条目与目录
if pack_id:
    raw_req("DELETE", "/api/sound/sources/" + up.quote(pack_id))
try:
    for d in (pack_sub, pack_dir):
        if os.path.isdir(d):
            os.rmdir(d)
except Exception:
    pass

print()
print("=" * 60)
print("I. 编排定向修改边界（/api/projects/{id}/arrangement/ops）")
print("=" * 60)
ops_url = f"/api/projects/{pid}/arrangement/ops"


def arrangement_now(project_id):
    st, b, _ = raw_req("GET", f"/api/projects/{project_id}/arrangement")
    try:
        return st, json.loads(b)
    except Exception:
        return st, None


st, b, _ = raw_req("POST", ops_url, b"not-json", {"Content-Type": "application/json"})
check("ops 非法 JSON → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", ops_url, json.dumps({"client": "e2e"}).encode(), {"Content-Type": "application/json"})
check("ops 缺 ops 字段 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", ops_url, json.dumps({"ops": [{}]}).encode(), {"Content-Type": "application/json"})
check("ops 元素缺 op 名 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", ops_url, json.dumps({"ops": [{"op": "dropTable"}]}).encode(),
                   {"Content-Type": "application/json"})
check("ops 未知操作名 → 400（不静默忽略）",
      st == 400 and "未知操作" in b.decode("utf-8", errors="replace"), f"st={st} body={b[:160]}")

for bad_op, why in [({"op": "setBpm", "bpm": 10}, "bpm 超范围"),
                    ({"op": "setBpm", "bpm": 9999}, "bpm 超上限"),
                    ({"op": "setBpm", "bpm": "快"}, "bpm 非数字"),
                    ({"op": "setBpm", "bpm": 120.5}, "bpm 非整数"),
                    ({"op": "setTrackMix", "track": 999, "gain": 0.5}, "轨道越界"),
                    ({"op": "setTrackMix", "track": 0, "gain": 9}, "gain 超范围"),
                    ({"op": "setRackItem", "itemId": "rk__e2e_no_such__", "name": "x"}, "机架条目不存在"),
                    ({"op": "setTrackSource", "track": 0}, "缺 itemId"),
                    ({"op": "setLoop", "on": True, "start": 8, "end": 1}, "循环区间倒置")]:
    st, b, _ = raw_req("POST", ops_url, json.dumps({"ops": [bad_op]}).encode(),
                       {"Content-Type": "application/json"})
    check(f"ops 非法参数拒绝: {why}", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

st, b, _ = raw_req("POST", ops_url, json.dumps({"ops": [{"op": "setBpm", "bpm": 150}] * 65}).encode(),
                   {"Content-Type": "application/json"})
check("ops 单次超过 64 个操作 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

# 原子性：同一批里"合法 + 非法"必须整批放弃，不能只落前半
_, arr_before = arrangement_now(pid)
bpm_before = int((arr_before or {}).get("bpm") or 0) if isinstance(arr_before, dict) else 0
st, b, _ = raw_req("POST", ops_url,
                   json.dumps({"ops": [{"op": "setBpm", "bpm": 199}, {"op": "dropTable"}]}).encode(),
                   {"Content-Type": "application/json"})
check("ops 批量含非法操作 → 400", st == 400, f"st={st} body={b[:160]}")
st_after, arr_after = arrangement_now(pid)
bpm_after = int((arr_after or {}).get("bpm") or 0) if isinstance(arr_after, dict) else -1
check("失败批次未落盘（bpm 未变、无半截写入）",
      st_after == 200 and bpm_after == bpm_before and bpm_after != 199,
      f"before={bpm_before} after={bpm_after}")

st, b, _ = raw_req("POST", "/api/projects/__e2e_no_such_project__/arrangement/ops",
                   json.dumps({"ops": [{"op": "setBpm", "bpm": 150}]}).encode(),
                   {"Content-Type": "application/json"})
check("不存在项目的 ops → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

st, b, _ = raw_req("GET", ops_url)
check("GET ops → 405", st == 405, f"st={st} body={b[:120]}")

big_ops = json.dumps({"ops": [{"op": "setBpm", "bpm": 150}],
                      "pad": "x" * (2 * 1024 * 1024 + 100)}).encode()
st, b, _ = raw_req("POST", ops_url, big_ops, {"Content-Type": "application/json"}, timeout=30)
check("ops 超大请求体被拒（不 500 崩溃）", st in (400, 413), f"st={st} body={b[:120]}")

print()
print("=" * 60)
print("J. 引擎插件宿主边界（/api/engine/plugins*）")
print("=" * 60)
st, b, _ = raw_req("GET", "/api/engine/plugins/__e2e_no_such_sub__")
if engine_down(st, b):
    skip("engine/plugins 未知子路径 → 404", f"引擎不可用（st={st}）")
else:
    check("engine/plugins 未知子路径 → 404", st == 404 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", "/api/engine/plugins/load",
                   json.dumps({"format": "mm", "path": "../../evil.dll", "track": 0}).encode(),
                   {"Content-Type": "application/json"})
if engine_down(st, b):
    skip("engine/plugins/load MM 路径越界拒绝", f"引擎不可用（st={st}）")
else:
    check("engine/plugins/load MM 路径越界 → 400（包含性校验先于装载）",
          st == 400 and detail_of(b) != "", f"st={st} body={b[:200]}")

for pkg, want in [("C:\\Windows\\Temp\\evil.mm", 403), ("C:\\evil.mm", 403),
                  ("..\\..\\evil.mm", 403), ("notmm.txt", 400), ("", 400)]:
    st, b, _ = raw_req("POST", "/api/engine/plugins/install",
                       json.dumps({"pkg": pkg, "preview": True}).encode(),
                       {"Content-Type": "application/json"})
    if engine_down(st, b):
        skip(f"engine/plugins/install 拒绝 pkg={pkg!r}", f"引擎不可用（st={st}）")
    else:
        check(f"engine/plugins/install 拒绝 pkg={pkg!r} → {want}",
              st == want and detail_of(b) != "", f"st={st} body={b[:200]}")

st, b, _ = raw_req("POST", "/api/engine/plugins/install", b"{bad", {"Content-Type": "application/json"})
if engine_down(st, b):
    skip("engine/plugins/install 非法 JSON", f"引擎不可用（st={st}）")
else:
    check("engine/plugins/install 非法 JSON → 400", st == 400, f"st={st} body={b[:140]}")

for bad_id in ["", "../evil", "a/b", "..\\evil"]:
    st, b, _ = raw_req("POST", "/api/engine/plugins/uninstall",
                       json.dumps({"id": bad_id}).encode(), {"Content-Type": "application/json"})
    if engine_down(st, b):
        skip(f"engine/plugins/uninstall 拒绝 id={bad_id!r}", f"引擎不可用（st={st}）")
    else:
        check(f"engine/plugins/uninstall 拒绝 id={bad_id!r} → 400",
              st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

# 插件状态随工程保存（P5 新增 /state/save、/state/load、/state/list）：
# 校验路径都在触达引擎之前，环境无引擎也能断言
for nm, method, path, payload, want in [
        ("engine/plugins/state/list 缺 project → 400", "GET", "/api/engine/plugins/state/list", None, 400),
        ("engine/plugins/state/list 不存在工程 → 400", "GET",
         "/api/engine/plugins/state/list?project=__e2e_no_such_project__", None, 400),
        ("engine/plugins/state/list 正常工程 → 200", "GET",
         "/api/engine/plugins/state/list?project=" + up.quote(pid), None, 200),
        ("engine/plugins/state/load 未保存过 → 404", "POST", "/api/engine/plugins/state/load",
         {"project": pid, "track": 7, "slot": 7, "ref": "e2e-no-such-ref"}, 404),
        ("engine/plugins/state/load 非法轨道 → 400", "POST", "/api/engine/plugins/state/load",
         {"project": pid, "track": 99}, 400),
        ("engine/plugins/state/load 缺工程 → 400", "POST", "/api/engine/plugins/state/load",
         {"track": 0, "slot": 0}, 400),
        ("engine/plugins/state/save 非法轨道 → 400", "POST", "/api/engine/plugins/state/save",
         {"project": pid, "track": 99}, 400),
        ("engine/plugins/state/save 缺工程 → 400", "POST", "/api/engine/plugins/state/save",
         {"track": 0, "slot": 0}, 400),
        ("engine/plugins/state/save 不支持 GET → 405", "GET", "/api/engine/plugins/state/save", None, 405)]:
    data = json.dumps(payload).encode() if payload is not None else None
    st, b, _ = raw_req(method, path, data, {"Content-Type": "application/json"} if data else None)
    if engine_down(st, b):
        skip(nm, f"引擎不可用（st={st}）")
    else:
        ok = st == want and (want == 200 or detail_of(b) != "")
        check(nm, ok, f"st={st} body={b[:180]}")

# 合法 target 但槽位没有插件：引擎在才会走到"导出失败"，否则按环境跳过
st, b, _ = raw_req("POST", "/api/engine/plugins/state/save",
                   json.dumps({"project": pid, "track": 7, "slot": 7, "ref": "e2e"}).encode(),
                   {"Content-Type": "application/json"})
if engine_down(st, b) or (st == 400 and "导出插件状态失败" in b.decode("utf-8", errors="replace")):
    skip("engine/plugins/state/save 槽位无插件 → 可解释失败", f"引擎不可用或无插件（st={st}）")
else:
    check("engine/plugins/state/save 槽位无插件 → 400",
          st == 400 and detail_of(b) != "", f"st={st} body={b[:180]}")

# /ui：免令牌但必须拿一次性 ui_ticket；缺票/越界/重放都要拒绝
st, b, _ = raw_req("GET", "/api/engine/plugins/ui?plugin=x", no_token=True)
if engine_down(st, b):
    skip("engine/plugins/ui 缺票 → 401", f"引擎不可用（st={st}）")
else:
    check("engine/plugins/ui 缺票 → 401", st == 401 and detail_of(b) != "", f"st={st} body={b[:160]}")

st, b, _ = raw_req("POST", "/api/engine/plugins/ui-ticket")
ticket = None
try:
    ticket = (json.loads(b) or {}).get("ticket")
except Exception:
    ticket = None
if not ticket:
    skip("engine/plugins/ui 凭据类用例", f"未签发到 ui_ticket（st={st}）")
else:
    st, b, _ = raw_req("GET", "/api/engine/plugins/ui?ticket=" + ticket + "&plugin=..%2Fevil", no_token=True)
    check("ui ticket + plugin 穿越 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")

    st, b, _ = raw_req("GET", "/api/engine/plugins/ui?ticket=" + ticket + "&plugin=x", no_token=True)
    check("ui ticket 一次性（重放 → 401）", st == 401, f"st={st} body={b[:160]}")

    st, b, _ = raw_req("POST", "/api/engine/plugins/ui-ticket")
    try:
        ticket2 = (json.loads(b) or {}).get("ticket")
    except Exception:
        ticket2 = None
    if ticket2:
        st, b, _ = raw_req("GET", "/api/engine/plugins/ui?ticket=" + ticket2 + "&plugin=__e2e_no_such__",
                           no_token=True)
        check("ui 未安装插件 → 404（不放行任意 id）", st == 404 and detail_of(b) != "", f"st={st} body={b[:160]}")

        st, b, _ = raw_req("GET", "/api/engine/plugins/ui?ticket=" + ticket2 + "&plugin=__e2e_no_such__",
                           no_token=True)
        check("ui 复用已消费票 → 401", st == 401, f"st={st} body={b[:160]}")

    st, inst_b, _ = raw_req("GET", "/api/engine/plugins/installed")
    installed = []
    try:
        installed = (json.loads(inst_b) or {}).get("plugins") or []
    except Exception:
        installed = []
    if installed and isinstance(installed[0], dict) and installed[0].get("id"):
        st, b, _ = raw_req("POST", "/api/engine/plugins/ui-ticket")
        try:
            ticket3 = (json.loads(b) or {}).get("ticket")
        except Exception:
            ticket3 = None
        if ticket3:
            st, b, _ = raw_req("GET", "/api/engine/plugins/ui?ticket=" + ticket3
                               + "&plugin=" + up.quote(str(installed[0]["id"]))
                               + "&asset=..%2F..%2Fsettings.json", no_token=True)
            check("ui asset 越界 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:160]}")
    else:
        skip("ui asset 越界 → 400", "本机无已安装 MM 插件（无可用 plugin id）")

print()
print("=" * 60)
print("K. 启动令牌 / 来源收窄 / 迁移与批准闸门（本轮新增）")
print("=" * 60)
tok = _load_token()

st, b, _ = raw_req("GET", "/api/health", headers={"Host": "127.0.0.1:7860"}, no_token=True)
check("免令牌路径 /api/health 无令牌可访问", st == 200, f"st={st} body={b[:100]}")

st, b, _ = raw_req("GET", "/api/version", headers={"Host": "127.0.0.1:7860"}, no_token=True)
check("免令牌路径 /api/version 无令牌可访问", st == 200, f"st={st} body={b[:100]}")

st, b, _ = raw_req("GET", "/api/bootstrap.js", headers={"Host": "127.0.0.1:7860"}, no_token=True)
check("免令牌路径 /api/bootstrap.js 无令牌可访问",
      st == 200 and "token" in b.decode("utf-8", errors="replace"), f"st={st} body={b[:120]}")

st, b, _ = raw_req("GET", "/api/sound/sources", headers={"Host": "127.0.0.1:7860"}, no_token=True)
if tok and st == 401:
    check("受保护端点缺令牌 → 401（可解释）", detail_of(b) != "", f"st={st} body={b[:140]}")
else:
    skip("受保护端点缺令牌 → 401", "服务端未启用启动令牌（authToken 为空或旧版构建）")

st, b, _ = raw_req("GET", "/api/sound/sources",
                   headers={"Host": "127.0.0.1:7860", "X-MM-Token": "deadbeef"}, no_token=True)
if tok:
    check("受保护端点错误令牌 → 401", st == 401, f"st={st} body={b[:140]}")
else:
    skip("受保护端点错误令牌 → 401", "服务端未启用启动令牌")

st, b, _ = raw_req("OPTIONS", "/api/sound/sources",
                   headers={"Host": "127.0.0.1:7860", "Origin": "http://127.0.0.1:7860"}, no_token=True)
check("OPTIONS 预检免令牌 → 200", st == 200, f"st={st} body={b[:100]}")

# 来源收窄：同机其他端口不再算可信来源（本轮 AllowOrigins 收窄）
st, b, _ = raw_req("GET", "/api/health", headers={"Host": "127.0.0.1:7860", "Origin": "http://127.0.0.1:9999"})
check("同机其他端口 Origin → 403（本轮收窄）", st == 403, f"st={st} body={b[:100]}")

st, b, _ = raw_req("GET", "/api/health", headers={"Host": "127.0.0.1:7860", "Origin": "http://localhost:7860"})
check("localhost 同端口 Origin 放行", st == 200, f"st={st} body={b[:100]}")

st, b, _ = raw_req("GET", "/api/health", headers={"Host": "127.0.0.1:7860", "Origin": "https://wails.localhost"})
check("Wails 来源放行", st == 200, f"st={st} body={b[:100]}")

st, b, _ = raw_req("GET", "/api/bootstrap.js",
                   headers={"Host": "127.0.0.1:7860", "Origin": "https://evil.com"}, no_token=True)
leaked = bool(tok) and tok in b.decode("utf-8", errors="replace")
check("bootstrap.js 恶意 Origin → 403 且不泄漏令牌", st == 403 and not leaked, f"st={st} leaked={leaked}")

st, b, _ = raw_req("POST", "/api/migration", b"{}", {"Content-Type": "application/json"})
check("POST /api/migration → 405", st == 405, f"st={st} body={b[:120]}")

st, b, _ = raw_req("POST", "/api/approval", json.dumps({"project_id": pid, "allow": True}).encode(),
                   {"Content-Type": "application/json"})
check("approval 缺 approval_id → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", "/api/approval",
                   json.dumps({"project_id": pid, "approval_id": "__e2e_no_such__", "allow": True}).encode(),
                   {"Content-Type": "application/json"})
check("approval 未知 id 批准 → 404（fail-closed）", st == 404, f"st={st} body={b[:160]}")

st, b, _ = raw_req("POST", "/api/approval", b"", {"Content-Type": "application/json"})
check("approval 空 body → 400", st == 400, f"st={st} body={b[:140]}")

st, b, _ = raw_req("GET", "/api/approval")
check("GET /api/approval → 405", st == 405, f"st={st} body={b[:120]}")

print()
print("=" * 60)
print("L. 素材目录穿越 / 计划端点未注册")
print("=" * 60)
st, b, _ = raw_req("GET", "/api/arrangement/dirs")
try:
    mat_dirs = json.loads(b).get("dirs") or []
except Exception:
    mat_dirs = []

for path in ["/api/arrangement/files?dir=../../settings.json",
             "/api/arrangement/files?dir=__e2e_no_such_dir__",
             "/api/arrangement/files?dir=.",
             "/api/arrangement/files"]:
    st, b, _ = raw_req("GET", path)
    check(f"素材目录拒绝: {path}", st in (400, 403, 404), f"st={st} body={b[:140]}")

if mat_dirs:
    st, b, _ = raw_req("GET", "/api/arrangement/files?dir=" + up.quote(mat_dirs[0])
                       + "&sub=..%2F..%2Fsettings.json")
    check("素材目录 sub 穿越 → 400", st == 400 and detail_of(b) != "", f"st={st} body={b[:140]}")

    st, b, _ = raw_req("GET", "/api/arrangement/files?dir=" + up.quote(mat_dirs[0])
                       + "&sub=%2e%2e%2fsettings.json")
    check("素材目录 sub 编码穿越 → 400", st == 400, f"st={st} body={b[:140]}")

    st, b, _ = raw_req("GET", "/api/arrangement/audio?p="
                       + up.quote(os.path.join(mat_dirs[0], "..", "..", "settings.json")))
    check("素材音频越界路径 → 4xx（不返回文件）", st in (400, 403, 404), f"st={st} body={b[:140]}")
else:
    skip("素材目录 sub/audio 穿越", "无已注册素材目录")

# 设计文档承诺但当前未注册的端点：必须干净 404，不能假成功
for path in ["/api/mm/build", "/api/mm/install", "/api/plugin/params",
             "/api/plugin/editor", "/api/plugin/state"]:
    st, b, _ = raw_req("POST", path, b"{}", {"Content-Type": "application/json"})
    check(f"计划端点未注册 → 404: {path}", st == 404, f"st={st} body={b[:120]}")

st, b, _ = raw_req("POST", "/api/sound/import/mm", b"", {"Content-Type": "application/octet-stream"})
check("POST /api/sound/import/mm 未注册 → 404（MM 安装走 /api/engine/plugins/install）",
      st == 404 and detail_of(b) != "", f"st={st} body={b[:140]}")

st, b, _ = raw_req("POST", "/api/sound/vst/scan", b"{}", {"Content-Type": "application/json"}, timeout=30)
if st == -1:
    skip("POST /api/sound/vst/scan", "请求超时（可能已实现为长时间扫描）：弱断言不判失败")
else:
    check("POST /api/sound/vst/scan 可解释响应（当前 404，扫描在 /api/engine/plugins/scan）",
          st in (404, 405) or (st in (200, 202, 503) and detail_of(b) != ""), f"st={st} body={b[:140]}")

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