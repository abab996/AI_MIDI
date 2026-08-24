import os
import sys
import subprocess
from collections import defaultdict
from pathlib import Path

ROOT = Path(r"D:\pyx\AI_MIDI-go")

def is_comment(line, ext):
    line = line.strip()
    if not line:
        return False
    if ext in ['.go', '.js', '.ts', '.jsx', '.tsx', '.cpp', '.c', '.h', '.hpp', '.cc', '.cxx', '.java', '.cs']:
        return line.startswith('//') or line.startswith('/*') or line.startswith('*')
    elif ext in ['.py', '.sh', '.bash', '.ps1']:
        return line.startswith('#')
    elif ext in ['.bat', '.cmd']:
        return line.upper().startswith('REM') or line.startswith('::')
    elif ext in ['.html', '.xml', '.svg']:
        return line.startswith('<!--')
    elif ext in ['.css', '.scss', '.less']:
        return line.startswith('/*') or line.startswith('*')
    return False

def count_file(filepath):
    ext = filepath.suffix.lower()
    total = blank = comment = code = 0
    try:
        with open(filepath, 'r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                total += 1
                s = line.strip()
                if not s:
                    blank += 1
                elif is_comment(s, ext):
                    comment += 1
                else:
                    code += 1
    except Exception:
        pass
    return {'files': 1, 'code': code, 'comment': comment, 'blank': blank, 'total': total}

def classify_lang(filepath):
    ext = filepath.suffix.lower()
    name = filepath.name.lower()
    if ext == '.go':
        return 'Go'
    elif ext in ['.js', '.jsx', '.ts', '.tsx', '.mjs', '.cjs']:
        return 'JS / TS'
    elif ext in ['.css', '.scss', '.less']:
        return 'CSS'
    elif ext in ['.html', '.htm']:
        return 'HTML'
    elif ext in ['.c', '.cpp', '.cc', '.cxx', '.h', '.hpp']:
        return 'C/C++'
    elif ext == '.py':
        return 'Python'
    elif ext in ['.md', '.markdown']:
        return 'Docs/Markdown'
    elif ext in ['.bat', '.cmd', '.ps1', '.sh', '.bash']:
        return 'Bat/Scripts'
    elif ext in ['.json', '.yaml', '.yml', '.toml', '.ini', '.txt', '.mod', '.sum']:
        return 'Config/Data'
    return 'Other'

# We should define what is first-party vs third-party/venv
# Let's inspect which files belong to first-party
# First-party excludes:
# - engine/ThirdParty
# - engine/build
# - 原版Python后端 (contains .venv / third party pip packages if any)
# Let's check 原版Python后端

