"""Tests for ``backend.state.local_state_store.LocalStateStore``.

Covers (per B3 Task 3):

* first save (revision None -> new revision, no conflict)
* update with matching revision
* stale revision conflict
* local migration idempotency (ensure_schema twice)
* database rename identity mapping
  layer (see test_grid_state_service.py); here we test the store primitives.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from backend.state import local_state_store
from backend.state.local_state_store import LocalStateStore


@pytest.fixture
def store(tmp_path: Path) -> LocalStateStore:
    s = LocalStateStore(db_path=tmp_path / "state.db")
    yield s
    s.close()


def test_first_save_returns_new_revision_no_conflict(store: LocalStateStore) -> None:
    payload = {"columns": [{"name": "amount", "width": 120}]}
    revision, conflict = store.save_grid_state(
        database_id="db1", table="contracts", payload=payload, revision=None
    )
    assert conflict is False
    assert revision  # non-empty


def test_update_with_matching_revision(store: LocalStateStore) -> None:
    payload1 = {"columns": [{"name": "amount", "width": 120}]}
    rev1, conflict1 = store.save_grid_state(
        database_id="db1", table="contracts", payload=payload1, revision=None
    )
    assert conflict1 is False
    payload2 = {"columns": [{"name": "amount", "width": 140}]}
    rev2, conflict2 = store.save_grid_state(
        database_id="db1", table="contracts", payload=payload2, revision=rev1
    )
    assert conflict2 is False
    assert rev2 != rev1
    # The stored payload is the updated one.
    loaded, loaded_rev = store.get_grid_state(database_id="db1", table="contracts")
    assert loaded == payload2
    assert loaded_rev == rev2


def test_stale_revision_conflict(store: LocalStateStore) -> None:
    payload1 = {"columns": []}
    rev1, _ = store.save_grid_state(
        database_id="db1", table="contracts", payload=payload1, revision=None
    )
    # Save again with the correct revision, advancing it.
    _, _ = store.save_grid_state(
        database_id="db1", table="contracts", payload={"columns": []}, revision=rev1
    )
    # Now try to save with the stale rev1 -> conflict.
    _, conflict = store.save_grid_state(
        database_id="db1",
        table="contracts",
        payload={"columns": [{"name": "x"}]},
        revision=rev1,
    )
    assert conflict is True


def test_get_returns_none_when_absent(store: LocalStateStore) -> None:
    payload, revision = store.get_grid_state(database_id="missing", table="nope")
    assert payload is None
    assert revision is None


def test_migration_idempotent(tmp_path: Path) -> None:
    """Running ensure_schema twice is a no-op (no error, schema unchanged)."""
    path = tmp_path / "state.db"
    store = LocalStateStore(db_path=path)
    store.ensure_schema()
    store.ensure_schema()
    # Still works after double-migration.
    rev, conflict = store.save_grid_state(
        database_id="db1", table="t", payload={"x": 1}, revision=None
    )
    assert conflict is False
    assert rev
    store.close()


def test_database_rename_identity_mapping(store: LocalStateStore) -> None:
    """Re-keying a database identity moves saved state to the new id."""
    store.save_grid_state(database_id="old-id", table="contracts", payload={"a": 1}, revision=None)
    count = store.rename_database_identity(old_id="old-id", new_id="new-id")
    assert count == 1
    payload, _ = store.get_grid_state(database_id="new-id", table="contracts")
    assert payload == {"a": 1}
    payload_old, _ = store.get_grid_state(database_id="old-id", table="contracts")
    assert payload_old is None


def test_profiles_are_isolated(store: LocalStateStore) -> None:
    """Different profiles get independent state rows."""
    store.save_grid_state(
        database_id="db1",
        table="t",
        payload={"p": "default"},
        revision=None,
        profile="default",
    )
    store.save_grid_state(
        database_id="db1",
        table="t",
        payload={"p": "alice"},
        revision=None,
        profile="alice",
    )
    default_payload, _ = store.get_grid_state(database_id="db1", table="t", profile="default")
    alice_payload, _ = store.get_grid_state(database_id="db1", table="t", profile="alice")
    assert default_payload == {"p": "default"}
    assert alice_payload == {"p": "alice"}


def test_first_save_with_nonnull_revision_on_empty_is_conflict(
    store: LocalStateStore,
) -> None:
    """Claiming a revision when no row exists yet is a conflict."""
    _, conflict = store.save_grid_state(
        database_id="db1", table="t", payload={"x": 1}, revision="bogus"
    )
    assert conflict is True


def test_store_is_thread_safe(tmp_path: Path) -> None:
    """Concurrent saves from two threads do not corrupt the store."""
    import threading

    path = tmp_path / "state.db"
    store = LocalStateStore(db_path=path)
    results: list[tuple[str, bool]] = []

    def save(worker_id: str) -> None:
        _rev, conflict = store.save_grid_state(
            database_id="db1", table="t", payload={"w": worker_id}, revision=None
        )
        results.append((worker_id, conflict))

    t1 = threading.Thread(target=save, args=("w1",))
    t2 = threading.Thread(target=save, args=("w2",))
    t1.start()
    t2.start()
    t1.join()
    t2.join()
    assert len(results) == 2
    assert sorted(conflict for _, conflict in results) == [False, True]
    payload, revision = store.get_grid_state(database_id="db1", table="t")
    assert payload is not None
    assert payload["w"] in {"w1", "w2"}
    assert revision
    store.close()


def test_default_path_and_singleton_reset_are_isolated(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path))
    expected = tmp_path / "VibeTable" / "state" / "vibetable-state.db"
    assert local_state_store.default_state_db_path() == expected
    assert expected.parent.is_dir()

    first = local_state_store.reset_local_state_store_for_tests(tmp_path / "first.db")
    assert local_state_store.get_local_state_store() is first
    second = local_state_store.reset_local_state_store_for_tests(tmp_path / "second.db")
    assert second is not first
    assert local_state_store.get_local_state_store() is second
    second.close()
    monkeypatch.setattr(local_state_store, "_SINGLETON", None)


def test_explicit_state_dir_overrides_local_app_data_and_creates_parent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    state_dir = tmp_path / "workspace-state" / "nested"
    monkeypatch.setenv("VIBETABLE_STATE_DIR", str(state_dir))
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "ignored-local-app-data"))

    expected = state_dir / "vibetable-state.db"
    assert local_state_store.default_state_db_path() == expected
    assert expected.parent.is_dir()


def test_default_path_falls_back_to_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("LOCALAPPDATA", raising=False)
    monkeypatch.setattr(Path, "home", classmethod(lambda cls: tmp_path))
    assert local_state_store.default_state_db_path() == (
        tmp_path / ".vibetable" / "state" / "vibetable-state.db"
    )


def test_get_singleton_initializes_default_store(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path))
    monkeypatch.setattr(local_state_store, "_SINGLETON", None)
    singleton = local_state_store.get_local_state_store()
    assert singleton is local_state_store.get_local_state_store()
    singleton.close()
    monkeypatch.setattr(local_state_store, "_SINGLETON", None)
