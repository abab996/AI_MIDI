import os
import re
import subprocess
from collections import defaultdict
from pathlib import Path

ROOT = Path(r"D:\pyx\AI_MIDI-go")

# Directories/patterns to ignore:
IGNORE_DIRS = {
    ".git", ".trae", ".workbuddy", ".zcode", "node_modules", "dist", "bin",
    "build", "output", "Library", ".idea", ".vscode", "__pycache__",
    "ThirdParty", "wailsjs"  # wailsjs is auto-generated bindings, ThirdParty in engine is external deps (JUCE, etc.)
}

IGNORE_FILES = {
    "go.sum", "package-lock.json", "smoke_run.log", "e2e_run.log", "eng_out.log",
    "inspect_stats.py"
}

# Binary / non-code extensions to ignore:
IGNORE_EXTS = {
    ".ico", ".png", ".jpg", ".jpeg", ".gif", ".exe", ".dll", ".lib", ".obj",
    ".pdb", ".tar", ".gz", ".zip", ".7z", ".mp3", ".wav", ".mid", ".midi",
    ".sf2", ".sf3", ".woff", ".woff2", ".ttf", ".eot"
}

# Language classification:
# 1. Lines of code by language (Go, JS, CSS, HTML, C/C++, Python, Docs/Markdown, Bat/Scripts)
EXT_LANG_MAP = {
    ".go": "Go",
    ".js": "JS",
    ".jsx": "JS",
    ".ts": "JS / TS",
    ".tsx": "JS / TS",
    ".css": "CSS",
    ".scss": "CSS",
    ".less": "CSS",
    ".html": "HTML",
    ".htm": "HTML",
    ".c": "C/C++",
    ".cc": "C/C++",
    ".cpp": "C/C++",
    ".cxx": "C/C++",
    ".h": "C/C++",
    ".hpp": "C/C++",
    ".hxx": "C/C++",
    ".py": "Python",
    ".pyw": "Python",
    ".md": "Docs/Markdown",
    ".markdown": "Docs/Markdown",
    ".bat": "Bat/Scripts",
    ".cmd": "Bat/Scripts",
    ".ps1": "Bat/Scripts",
    ".sh": "Bat/Scripts",
    ".json": "JSON / Config",
    ".toml": "JSON / Config",
    ".yaml": "JSON / Config",
    ".yml": "JSON / Config",
    ".txt": "Docs/Text",
}

# Comment syntax per language type:
# returns (line_comment_prefixes, block_comment_start, block_comment_end)
def get_comment_syntax(lang):
    if lang in ("Go", "JS", "JS / TS", "CSS", "C/C++"):
        return (["//"], "/*", "*/")
    elif lang in ("Python", "Docs/Text"):
        return (["#"], '"""', '"""') # simplified
    elif lang == "Bat/Scripts":
        return (["REM", "rem", "::", "#"], None, None)
    elif lang == "HTML":
        return ([], "<!--", "-->")
    else:
        return ([], None, None)

def analyze_file(filepath, lang):
    try:
        with open(filepath, "r", encoding="utf-8", errors="ignore") as f:
            lines = f.readlines()
    except Exception:
        return 0, 0, 0

    total_lines = len(lines)
    blank_lines = 0
    comment_lines = 0
    code_lines = 0

    line_prefixes, block_start, block_end = get_comment_syntax(lang)
    in_block_comment = False

    for raw_line in lines:
        line = raw_line.strip()
        if not line:
            blank_lines += 1
            continue

        if block_start and block_end:
            if in_block_comment:
                comment_lines += 1
                if block_end in line:
                    in_block_comment = False
                continue
            elif line.startswith(block_start):
                comment_lines += 1
                if not (block_end in line and line.find(block_end) > line.find(block_start)):
                    in_block_comment = True
                continue

        if any(line.startswith(p) for p in line_prefixes):
            comment_lines += 1
            continue

        code_lines += 1

    return total_lines, blank_lines, comment_lines, code_lines

# Architectural Layer classification:
# 2. Breakdown by architectural layers:
#    internal/app, internal/engine, internal/server, internal/music (or other internal/*),
#    frontend, engine/native c++ if any, tools, root/others
def get_layer(rel_path_str):
    rel_path_str = rel_path_str.replace("\\", "/")
    parts = rel_path_str.split("/")

    if parts[0] == "internal":
        if len(parts) > 1:
            sub = parts[1]
            if sub in ("app", "engine", "server", "midi", "music", "chat", "config", "llm", "mcp", "tasks"):
                # if folder is midi vs music, check if requested as internal/music or internal/midi
                return f"internal/{sub}"
            return f"internal/{sub}"
        return "internal"
    elif parts[0] == "frontend":
        return "frontend"
    elif parts[0] == "engine":
        return "engine (native C++)"
    elif parts[0] == "tools":
        return "tools"
    elif parts[0] == "原版Python后端":
        return "原版Python后端 (Legacy Python)"
    elif parts[0] == "docs":
        return "docs"
    elif len(parts) == 1:
        return "root / config"
    else:
        return parts[0]

def scan_project():
    file_records = []
    
    for root, dirs, files in os.walk(ROOT):
        # filter out ignored dirs in-place
        dirs[:] = [d for d in dirs if d not in IGNORE_DIRS]

        for file in files:
            if file in IGNORE_FILES:
                continue
            ext = os.path.splitext(file)[1].lower()
            if ext in IGNORE_EXTS:
                continue

            full_path = Path(root) / file
            rel_path = full_path.relative_to(ROOT)
            rel_str = str(rel_path).replace("\\", "/")

            # check if any component is ignored
            if any(part in IGNORE_DIRS for part in rel_path.parts):
                continue

            lang = EXT_LANG_MAP.get(ext)
            if not lang:
                if file.startswith("Dockerfile") or file == "Makefile" or file == "CMakeLists.txt":
                    lang = "Bat/Scripts" if "CMake" not in file else "C/C++"
                else:
                    lang = "Other"

            tot, blank, comm, code = analyze_file(full_path, lang)
            layer = get_layer(rel_str)

            file_records.append({
                "path": rel_str,
                "ext": ext,
                "lang": lang,
                "layer": layer,
                "total": tot,
                "blank": blank,
                "comment": comm,
                "code": code
            })

    return file_records

records = scan_project()
print(f"Total scanned files: {len(records)}")
lang_stats = defaultdict(lambda: {"files": 0, "total": 0, "blank": 0, "comment": 0, "code": 0})
for r in records:
    l = r["lang"]
    lang_stats[l]["files"] += 1
    lang_stats[l]["total"] += r["total"]
    lang_stats[l]["blank"] += r["blank"]
    lang_stats[l]["comment"] += r["comment"]
    lang_stats[l]["code"] += r["code"]

for l, s in sorted(lang_stats.items(), key=lambda x: x[1]["code"], reverse=True):
    print(f"{l:15}: Files={s['files']:4}, Code={s['code']:6}, Comment={s['comment']:6}, Blank={s['blank']:6}, Total={s['total']:6}")
