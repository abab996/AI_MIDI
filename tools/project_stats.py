#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AI_MIDI-go 项目代码统计分析器 (TUI Engine)
"""

import os
import sys
import time

# ANSI 颜色定义
class Color:
    RESET = "\033[0m"
    BOLD = "\033[1m"
    DIM = "\033[2m"
    
    # 常用色
    CYAN = "\033[96m"
    BLUE = "\033[94m"
    GREEN = "\033[92m"
    YELLOW = "\033[93m"
    MAGENTA = "\033[95m"
    RED = "\033[91m"
    WHITE = "\033[97m"
    GRAY = "\033[90m"
    
    # 背景
    BG_BLUE = "\033[44m"
    BG_CYAN = "\033[46m"
    BG_DARK = "\033[100m"

# 启用 Windows 虚拟终端颜色支持
def enable_windows_ansi():
    if os.name == 'nt':
        import ctypes
        kernel32 = ctypes.windll.kernel32
        handle = kernel32.GetStdHandle(-11)
        mode = ctypes.c_ulong()
        kernel32.GetConsoleMode(handle, ctypes.byref(mode))
        kernel32.SetConsoleMode(handle, mode.value | 0x0004)

# 单文件行数统计
def count_lines(path):
    try:
        with open(path, 'r', encoding='utf-8', errors='ignore') as f:
            lines = 0
            code = 0
            comment = 0
            blank = 0
            for line in f:
                lines += 1
                stripped = line.strip()
                if not stripped:
                    blank += 1
                elif stripped.startswith('//') or stripped.startswith('#') or stripped.startswith('/*') or stripped.startswith('*'):
                    comment += 1
                else:
                    code += 1
            return lines, code, comment, blank
    except Exception:
        return 0, 0, 0, 0

# 模块划分配置
MODULES = [
    {
        "name": "Go 后端核心",
        "badge": "internal/",
        "match": lambda p: p.startswith("internal"),
        "color": Color.CYAN,
    },
    {
        "name": "Web 前端界面",
        "badge": "frontend/",
        "match": lambda p: p.startswith("frontend"),
        "color": Color.GREEN,
    },
    {
        "name": "C++ 音频引擎",
        "badge": "engine/Source/",
        "match": lambda p: p.startswith(os.path.normpath("engine/Source")),
        "color": Color.MAGENTA,
    },
    {
        "name": "测试与开发工具",
        "badge": "tools/",
        "match": lambda p: p.startswith("tools"),
        "color": Color.YELLOW,
    },
    {
        "name": "设计与开发文档",
        "badge": "docs/",
        "match": lambda p: p.startswith("docs"),
        "color": Color.BLUE,
    },
]

# 语言后缀配置
LANGUAGES = {
    'Go': {'.go'},
    'JavaScript': {'.js', '.mjs', '.cjs'},
    'C++ / Headers': {'.cpp', '.h', '.hpp', '.c'},
    'CSS / Style': {'.css', '.scss', '.less'},
    'HTML / Tpl': {'.html', '.htm'},
    'JSON / Data': {'.json'},
    'YAML / Config': {'.yaml', '.yml', '.toml'},
    'Markdown': {'.md'},
    'Scripts': {'.bat', '.sh', '.ps1'}
}

# 忽略排除的目录
EXCLUDE_DIRS = {
    '原版Python后端', '.git', 'build', 'external', 'JuceLibraryCode',
    'JUCE', 'bin', 'node_modules', '.idea', '.vscode', '.workbuddy', 
    '.trae', 'dist', 'out', '__pycache__', 'Library'
}

def render_bar(percentage, width=20, fill_color=Color.CYAN):
    filled = int(width * percentage / 100)
    bar = "█" * filled + "░" * (width - filled)
    return f"{fill_color}{bar}{Color.RESET}"

def get_git_info():
    branch = "未知"
    commit = "未知"
    try:
        import subprocess
        b = subprocess.check_output(['git', 'rev-parse', '--abbrev-ref', 'HEAD'], stderr=subprocess.DEVNULL).decode().strip()
        c = subprocess.check_output(['git', 'rev-parse', '--short', 'HEAD'], stderr=subprocess.DEVNULL).decode().strip()
        count = subprocess.check_output(['git', 'rev-list', '--count', 'HEAD'], stderr=subprocess.DEVNULL).decode().strip()
        if b: branch = b
        if c: commit = f"{c} (共 {count} 次提交)"
    except Exception:
        pass
    return branch, commit

def main():
    enable_windows_ansi()
    start_time = time.time()
    
    # 统计数据结构
    module_stats = {m["name"]: {"files": 0, "lines": 0, "code": 0, "comment": 0, "blank": 0} for m in MODULES}
    lang_stats = {lang: {"files": 0, "lines": 0, "code": 0} for lang in LANGUAGES}
    
    total_files = 0
    total_lines = 0
    total_code = 0
    total_comment = 0
    total_blank = 0

    for root, dirs, files in os.walk('.'):
        dirs[:] = [d for d in dirs if d not in EXCLUDE_DIRS]
        
        for file in files:
            ext = os.path.splitext(file)[1].lower()
            rel_path = os.path.relpath(os.path.join(root, file), '.')
            
            # 语言匹配
            matched_lang = None
            for lang, exts in LANGUAGES.items():
                if ext in exts:
                    matched_lang = lang
                    break
            
            if not matched_lang:
                continue
                
            # 模块匹配
            matched_module = None
            for mod in MODULES:
                if mod["match"](rel_path):
                    matched_module = mod["name"]
                    break
                    
            if not matched_module:
                continue

            lines, code, comment, blank = count_lines(rel_path)
            
            module_stats[matched_module]["files"] += 1
            module_stats[matched_module]["lines"] += lines
            module_stats[matched_module]["code"] += code
            module_stats[matched_module]["comment"] += comment
            module_stats[matched_module]["blank"] += blank
            
            lang_stats[matched_lang]["files"] += 1
            lang_stats[matched_lang]["lines"] += lines
            lang_stats[matched_lang]["code"] += code
            
            total_files += 1
            total_lines += lines
            total_code += code
            total_comment += comment
            total_blank += blank

    elapsed = time.time() - start_time
    branch, commit = get_git_info()

    # === TUI 渲染 ===
    W = 76  # 界面宽度
    
    print("\n" + Color.BOLD + Color.CYAN + " ╔" + "═" * (W - 2) + "╗" + Color.RESET)
    title = " 🎵  AI_MIDI-go 项目核心代码统计报告 (v3.0.1)  🎵 "
    print(Color.BOLD + Color.CYAN + f" ║{title.center(W - 2)}║" + Color.RESET)
    print(Color.BOLD + Color.CYAN + " ╠" + "═" * (W - 2) + "╣" + Color.RESET)
    
    # 基本信息行
    info_line1 = f"  分支状态: {Color.GREEN}{branch:<12}{Color.RESET}  Git 提交: {Color.YELLOW}{commit:<28}{Color.RESET}"
    print(f" ║ {info_line1}{' ' * (W - 48)}║")
    info_line2 = f"  核心架构: {Color.CYAN}Go (主服务){Color.RESET} + {Color.MAGENTA}C++/JUCE 8 (音频引擎){Color.RESET} + {Color.GREEN}WebUI (前端){Color.RESET}"
    print(f" ║ {info_line2}{' ' * (W - 74)}║")
    print(Color.CYAN + " ╠" + "═" * (W - 2) + "╣" + Color.RESET)

    # 1. 核心模块分布
    print(Color.BOLD + Color.WHITE + f" ║  📦  核心自研模块分布{' ' * (W - 24)}║" + Color.RESET)
    print(Color.GRAY + f" ║  {'模块名称':<16} {'文件数':<8} {'代码纯量':<10} {'总行数':<10} {'占比':<18}║" + Color.RESET)
    print(Color.GRAY + " ║  " + "─" * (W - 6) + "  ║" + Color.RESET)

    for mod in MODULES:
        name = mod["name"]
        st = module_stats[name]
        pct = (st["lines"] / total_lines * 100) if total_lines > 0 else 0
        bar = render_bar(pct, width=12, fill_color=mod["color"])
        
        row = (
            f" ║  {mod['color']}{name:<14}{Color.RESET} "
            f"{st['files']:>6} 个   "
            f"{st['code']:>8,} 行  "
            f"{st['lines']:>8,} 行  "
            f"{bar} {Color.WHITE}{pct:>5.1f}%{Color.RESET}  ║"
        )
        print(row)

    print(Color.CYAN + " ╠" + "═" * (W - 2) + "╣" + Color.RESET)

    # 2. 编程语言分布
    print(Color.BOLD + Color.WHITE + f" ║  💻  代码语言与技术栈占比{' ' * (W - 26)}║" + Color.RESET)
    print(Color.GRAY + f" ║  {'编程语言':<16} {'文件数':<8} {'代码纯量':<10} {'总行数':<10} {'占比':<18}║" + Color.RESET)
    print(Color.GRAY + " ║  " + "─" * (W - 6) + "  ║" + Color.RESET)

    active_langs = sorted(
        [(k, v) for k, v in lang_stats.items() if v["lines"] > 0],
        key=lambda x: x[1]["lines"],
        reverse=True
    )

    lang_colors = {
        'Go': Color.CYAN,
        'JavaScript': Color.YELLOW,
        'CSS / Style': Color.BLUE,
        'C++ / Headers': Color.MAGENTA,
        'HTML / Tpl': Color.RED,
        'JSON / Data': Color.GREEN,
        'Markdown': Color.WHITE,
        'YAML / Config': Color.GRAY,
        'Scripts': Color.GRAY,
    }

    for lang, st in active_langs:
        pct = (st["lines"] / total_lines * 100) if total_lines > 0 else 0
        c = lang_colors.get(lang, Color.WHITE)
        bar = render_bar(pct, width=12, fill_color=c)
        row = (
            f" ║  {c}{lang:<14}{Color.RESET} "
            f"{st['files']:>6} 个   "
            f"{st['code']:>8,} 行  "
            f"{st['lines']:>8,} 行  "
            f"{bar} {Color.WHITE}{pct:>5.1f}%{Color.RESET}  ║"
        )
        print(row)

    print(Color.CYAN + " ╠" + "═" * (W - 2) + "╣" + Color.RESET)

    # 3. 统计汇总
    print(Color.BOLD + Color.WHITE + f" ║  📊  核心代码总计汇总{' ' * (W - 24)}║" + Color.RESET)
    print(Color.GRAY + " ║  " + "─" * (W - 6) + "  ║" + Color.RESET)
    
    summary_1 = (
        f" ║  总文件数: {Color.BOLD}{Color.GREEN}{total_files:>5}{Color.RESET} 个     "
        f"纯代码行: {Color.BOLD}{Color.CYAN}{total_code:>7,}{Color.RESET} 行     "
        f"注释量: {Color.YELLOW}{total_comment:>6,}{Color.RESET} 行 ({total_comment/total_lines*100:.1f}%)"
    )
    print(f"{summary_1}{' ' * (W - 71)}║")
    
    summary_2 = (
        f" ║  空白行数: {Color.GRAY}{total_blank:>5,}{Color.RESET} 行     "
        f"总代码行: {Color.BOLD}{Color.MAGENTA}{total_lines:>7,}{Color.RESET} 行     "
        f"耗时: {Color.WHITE}{elapsed*1000:.1f} ms{Color.RESET}"
    )
    print(f"{summary_2}{' ' * (W - 61)}║")
    
    print(Color.BOLD + Color.CYAN + " ╚" + "═" * (W - 2) + "╝\n" + Color.RESET)

if __name__ == '__main__':
    main()
