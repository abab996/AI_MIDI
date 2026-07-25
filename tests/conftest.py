from __future__ import annotations

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))


@pytest.fixture
def tmp_project(tmp_path: Path) -> Path:
    for dirname in ("input", "output", "doing", "projects", "Library"):
        (tmp_path / dirname).mkdir()
    return tmp_path
