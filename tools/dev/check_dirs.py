import os
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

dirs_count = defaultdict(int)
for root, dirs, files in os.walk(ROOT):
    rel = Path(root).relative_to(ROOT)
    top = rel.parts[0] if rel.parts else "root"
    dirs_count[top] += len(files)

for k, v in sorted(dirs_count.items(), key=lambda x: x[1], reverse=True):
    print(f"{k:30}: {v} files")
