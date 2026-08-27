import os
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

print("=== internal subdirs ===")
for p in (ROOT / "internal").iterdir():
    if p.is_dir():
        files = list(p.rglob("*"))
        code_files = [f for f in files if f.is_file() and f.suffix in ['.go', '.js', '.ts', '.py', '.cpp', '.h', '.c']]
        print(f"internal/{p.name:15}: {len(code_files)} code files, {len([f for f in files if f.is_file()])} total files")

print("\n=== engine subdirs ===")
for p in (ROOT / "engine").iterdir():
    if p.is_dir():
        files = list(p.rglob("*"))
        print(f"engine/{p.name:15}: {len([f for f in files if f.is_file()])} total files")
