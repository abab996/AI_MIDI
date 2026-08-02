"""工作区绑定功能测试：project_manager 的 bind/unbind/sync/scan。"""
from __future__ import annotations

from pathlib import Path

import pytest

import project_manager


@pytest.fixture
def isolated_projects(tmp_path, monkeypatch):
    """把 project_manager 的 PROJECTS_DIR/INDEX_FILE 隔离到临时目录。"""
    monkeypatch.setattr(project_manager, "PROJECTS_DIR", tmp_path)
    monkeypatch.setattr(project_manager, "INDEX_FILE", tmp_path / "index.json")
    return tmp_path


def _make_midi(path: Path, content: bytes = b"MThd\x00\x00\x00\x06") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)


def test_scan_midi_files_recursive_and_filter(isolated_projects):
    base = isolated_projects / "ws"
    _make_midi(base / "a.mid")
    _make_midi(base / "sub" / "b.mid")
    _make_midi(base / "sub" / "deep" / "c.midi")
    (base / "note.txt").write_text("ignore", encoding="utf-8")  # 非 mid，过滤
    (base / "sub" / "readme.md").write_text("x", encoding="utf-8")

    files = project_manager.scan_midi_files(base)
    names = sorted(f["name"] for f in files)
    assert names == ["a.mid", "sub/b.mid", "sub/deep/c.midi"]
    assert all("path" in f and "size" in f for f in files)
    # path 是绝对路径
    assert Path(files[0]["path"]).is_absolute()


def test_bind_workspace_copies_and_renames(isolated_projects):
    meta = project_manager.create_project("p1")
    pid = meta["id"]
    _make_midi(project_manager.midi_dir(pid) / "song.mid")
    _make_midi(project_manager.midi_dir(pid) / "dup.mid")

    ws = isolated_projects / "workspace"
    _make_midi(ws / "dup.mid")  # 工作区已有 dup.mid -> 触发重名

    result = project_manager.bind_workspace(pid, str(ws))

    # 重名记录
    renamed_origs = [o for o, _ in result["renamed"]]
    assert "dup.mid" in renamed_origs
    # 工作区文件
    ws_names = sorted(p.name for p in ws.rglob("*.mid*"))
    assert "song.mid" in ws_names
    assert "dup_2.mid" in ws_names
    # midi_files 来自工作区扫描（relpath）
    mf_names = sorted(f["name"] for f in result["midi_files"])
    assert "song.mid" in mf_names and "dup_2.mid" in mf_names
    # meta 记录绑定
    assert project_manager.get_workspace_dir(pid) == str(ws.resolve())
    assert project_manager.get_midi_base_dir(pid) == ws.resolve()
    assert project_manager.get_midi_mirror_dir(pid) == project_manager.midi_dir(pid).resolve()


def test_unbind_workspace_keeps_files_reads_projects(isolated_projects):
    meta = project_manager.create_project("p2")
    pid = meta["id"]
    _make_midi(project_manager.midi_dir(pid) / "x.mid")
    ws = isolated_projects / "ws2"
    project_manager.bind_workspace(pid, str(ws))
    # 工作区外部加一个新文件
    _make_midi(ws / "y.mid")

    files = project_manager.unbind_workspace(pid)

    # 解绑后读 projects（只有 x.mid；y.mid 在工作区不读）
    assert sorted(f["name"] for f in files) == ["x.mid"]
    # 工作区文件保留不动
    assert (ws / "x.mid").exists()
    assert (ws / "y.mid").exists()
    assert project_manager.get_workspace_dir(pid) is None
    assert project_manager.get_midi_mirror_dir(pid) is None


def test_sync_workspace_to_projects_add_remove(isolated_projects):
    meta = project_manager.create_project("p3")
    pid = meta["id"]
    _make_midi(project_manager.midi_dir(pid) / "keep.mid")
    _make_midi(project_manager.midi_dir(pid) / "gone.mid")
    ws = isolated_projects / "ws3"
    project_manager.bind_workspace(pid, str(ws))
    # 工作区状态：删 gone.mid，加 new.mid（外部改动）
    (ws / "gone.mid").unlink()
    _make_midi(ws / "new.mid")

    files = project_manager.sync_workspace_to_projects(pid, [])

    ws_names = sorted(f["name"] for f in files)
    assert "gone.mid" not in ws_names
    assert "new.mid" in ws_names
    assert "keep.mid" in ws_names
    # projects 镜像同步：new.mid 复制过去，gone.mid 删除
    mirror = project_manager.midi_dir(pid)
    assert (mirror / "new.mid").exists()
    assert not (mirror / "gone.mid").exists()
    assert (mirror / "keep.mid").exists()


def test_sync_unbound_returns_prev_unchanged(isolated_projects):
    meta = project_manager.create_project("p4")
    pid = meta["id"]
    prev = [{"name": "x.mid", "path": "/tmp/x.mid", "size": 10, "note_table": "n"}]
    # 未绑定，sync 直接返回 prev，不操作磁盘
    assert project_manager.sync_workspace_to_projects(pid, prev) is prev


def test_copy_project_does_not_inherit_workspace_binding(isolated_projects):
    meta = project_manager.create_project("src")
    pid = meta["id"]
    _make_midi(project_manager.midi_dir(pid) / "a.mid")
    ws = isolated_projects / "ws_src"
    result = project_manager.bind_workspace(pid, str(ws))
    # bind_workspace 返回 midi_files 但不持久化，需手动 save_history
    project_manager.save_history(pid, [], result["midi_files"])

    copied = project_manager.copy_project(pid, "copy")
    new_id = copied["id"]
    # 副本不继承工作区绑定
    assert project_manager.get_workspace_dir(new_id) is None
    # 副本的 midi_files path 指向副本自己的 projects 目录
    _, midi_files = project_manager.load_history(new_id)
    assert midi_files
    for f in midi_files:
        assert str(project_manager.midi_dir(new_id)) in f["path"]
