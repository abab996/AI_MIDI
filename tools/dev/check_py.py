import os
from pathlib import Path

ROOT = Path(r"D:\pyx\AI_MIDI-go")
py_backend = ROOT / "原版Python后端"
if py_backend.exists():
    for item in py_backend.iterdir():
        print(item.name, "DIR" if item.is_dir() else "FILE")
