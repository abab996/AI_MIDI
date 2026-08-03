from __future__ import annotations

from unittest.mock import patch

import chat_service


def test_duplicate_project_names_keep_distinct_id_values():
    projects = [
        {"id": "project-a", "name": "同名项目", "message_count": 0},
        {"id": "project-b", "name": "同名项目", "message_count": 0},
    ]
    with patch.object(chat_service.project_manager, "list_projects", return_value=projects):
        choices, ids = chat_service._refresh_project_list()

    assert choices == [("同名项目", "project-a"), ("同名项目", "project-b")]
    assert ids == ["project-a", "project-b"]
    assert chat_service._resolve_project_id("project-b", ids) == "project-b"


def test_duplicate_file_labels_are_selected_by_path():
    files = [
        {"name": "same.mid", "path": "A/same.mid", "size": 10},
        {"name": "same.mid", "path": "B/same.mid", "size": 10},
    ]

    assert chat_service._selected_files(files, ["B/same.mid"]) == [files[1]]


def test_delete_persists_file_state_and_creates_undo_snapshot():
    files = [
        {"name": "a.mid", "path": "A/a.mid", "size": 10},
        {"name": "b.mid", "path": "B/b.mid", "size": 20},
    ]
    history = [{"role": "user", "content": "hello"}]

    with patch.object(chat_service, "_persist_project_state") as persist:
        updated, undo_stack, _ = chat_service._on_delete(
            files, ["B/b.mid"], [], "project-id", history,
        )

    assert updated == [files[0]]
    assert undo_stack == [files]
    persist.assert_called_once_with("project-id", history, updated)


def test_clear_chat_keeps_midi_metadata():
    files = [{"name": "song.mid", "path": "song.mid", "size": 10}]
    with patch.object(chat_service.project_manager, "save_history") as save:
        assert chat_service._on_clear_chat("project-id", files) == ([], [])
    save.assert_called_once_with("project-id", [], files)


def test_created_midi_path_rejects_output_escape(tmp_path):
    with patch.object(chat_service, "_current_project_id", None), patch.object(
        chat_service.config, "OUTPUT_DIR", tmp_path,
    ):
        try:
            chat_service._resolve_created_midi_path("../escape.mid")
        except ValueError:
            pass
        else:
            raise AssertionError("path traversal should be rejected")
