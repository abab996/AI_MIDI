import os
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
py_backend = ROOT / "原版Python后端"
if py_backend.exists():
    for item in py_backend.iterdir():
        print(item.name, "DIR" if item.is_dir() else "FILE")
