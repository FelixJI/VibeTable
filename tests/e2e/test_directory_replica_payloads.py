from __future__ import annotations

import json
import shutil
from pathlib import Path
from typing import BinaryIO

import pytest

from tests.e2e.directory_replica_payloads import (
    exchange_replica_payloads,
    seed_replica_payloads,
)

WORKSPACE_ID = "11111111-1111-4111-8111-111111111111"
REPLICA_ID = "22222222-2222-4222-8222-222222222222"
REMOTE = Path(".vibetable") / "replica-v2" / WORKSPACE_ID


def _write(root: Path, relative: Path | str, content: bytes) -> None:
    target = root / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(content)


def _replica(root: Path, *, with_payload: bool = True) -> Path:
    _write(
        root,
        ".vibetable/workspace.json",
        json.dumps(
            {"formatVersion": 2, "workspaceId": WORKSPACE_ID, "storageMode": "mirrored"}
        ).encode(),
    )
    _write(
        root,
        REMOTE / "identity.json",
        json.dumps(
            {
                "formatVersion": 1,
                "workspaceId": WORKSPACE_ID,
                "replicaId": REPLICA_ID,
                "strength": "advisory",
            }
        ).encode(),
    )
    if not with_payload:
        return root
    for directory, name in (
        ("publications", "base.json"),
        ("checkpoints", "base.json"),
        ("objects", "base.blob"),
        ("manifests", "base.json"),
    ):
        _write(root, REMOTE / directory / name, b"opaque product payload")
    return root


@pytest.mark.parametrize("empty_directory", [False, True])
def test_empty_replica_can_be_seeded_and_exchanged(tmp_path: Path, empty_directory: bool) -> None:
    source = _replica(tmp_path / "left", with_payload=False)
    if empty_directory:
        (source / REMOTE / "publications").mkdir()
    target = tmp_path / "right"

    assert len(seed_replica_payloads(source, target)) == 2
    result = exchange_replica_payloads(source, target)

    assert result.workspace_id == WORKSPACE_ID
    assert result.added_to_left == result.added_to_right == ()


def test_seed_copies_only_public_payload_into_an_empty_selected_root(tmp_path: Path) -> None:
    source = _replica(tmp_path / "left")
    _write(source, ".vibetable/coordination/write-coordinator.db", b"local authority")
    _write(source, "files/private.txt", b"not an immutable replica payload")
    target = tmp_path / "right"
    target.mkdir()

    copied = seed_replica_payloads(source, target)

    assert len(copied) == 6
    assert set(copied) == {
        str(path.relative_to(target).as_posix()) for path in target.rglob("*") if path.is_file()
    }
    assert (target / REMOTE / "objects/base.blob").read_bytes() == b"opaque product payload"
    assert not (target / ".vibetable/coordination").exists()
    assert not (target / "files").exists()


def test_exchange_adds_both_branches_without_changing_common_payload(tmp_path: Path) -> None:
    left = _replica(tmp_path / "left")
    right = tmp_path / "right"
    seed_replica_payloads(left, right)
    _write(left, REMOTE / "publications/left.json", b"left publication")
    _write(right, REMOTE / "objects/right.blob", b"right object")

    result = exchange_replica_payloads(left, right)

    assert result.workspace_id == WORKSPACE_ID
    assert result.added_to_left == ((REMOTE / "objects/right.blob").as_posix(),)
    assert result.added_to_right == ((REMOTE / "publications/left.json").as_posix(),)
    assert (left / REMOTE / "objects/right.blob").read_bytes() == b"right object"
    assert (right / REMOTE / "publications/left.json").read_bytes() == b"left publication"
    assert exchange_replica_payloads(left, right).added_to_left == ()


def test_unpublished_temporary_files_are_not_transported(tmp_path: Path) -> None:
    left = _replica(tmp_path / "left")
    temporary = REMOTE / "publications" / f".pending.json.{REPLICA_ID}.tmp"
    _write(left, temporary, b"unfinished publication")
    right = tmp_path / "right"

    assert len(seed_replica_payloads(left, right)) == 6
    assert exchange_replica_payloads(left, right).added_to_right == ()
    assert not (right / temporary).exists()
    assert (left / temporary).read_bytes() == b"unfinished publication"


def test_collision_fails_before_either_direction_writes(tmp_path: Path) -> None:
    left, right = _replica(tmp_path / "left"), _replica(tmp_path / "right")
    _write(left, REMOTE / "objects/base.blob", b"different bytes")
    _write(left, REMOTE / "publications/left.json", b"left")
    _write(right, REMOTE / "publications/right.json", b"right")

    with pytest.raises(ValueError, match="immutable payload differs"):
        exchange_replica_payloads(left, right)

    assert not (left / REMOTE / "publications/right.json").exists()
    assert not (right / REMOTE / "publications/left.json").exists()
    assert (left / REMOTE / "objects/base.blob").read_bytes() == b"different bytes"
    assert (right / REMOTE / "objects/base.blob").read_bytes() == b"opaque product payload"


def test_seed_refuses_nonempty_target_and_overlapping_roots(tmp_path: Path) -> None:
    source = _replica(tmp_path / "left")
    target = tmp_path / "right"
    _write(target, "keep.txt", b"keep")

    with pytest.raises(ValueError, match="empty"):
        seed_replica_payloads(source, target)
    with pytest.raises(ValueError, match="overlap"):
        seed_replica_payloads(source, source / "child")

    assert (target / "keep.txt").read_bytes() == b"keep"
    assert not (source / "child").exists()


@pytest.mark.parametrize("link_location", [".vibetable", "objects"])
def test_seed_refuses_linked_payload_before_creating_destination(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, link_location: str
) -> None:
    source = _replica(tmp_path / "left")
    linked = source / (".vibetable" if link_location == ".vibetable" else REMOTE / "objects")
    original = Path.is_junction
    monkeypatch.setattr(Path, "is_junction", lambda path: path == linked or original(path))

    with pytest.raises(ValueError, match="linked"):
        seed_replica_payloads(source, tmp_path / "right")

    assert not (tmp_path / "right").exists()


@pytest.mark.parametrize(
    ("identity_file", "field", "value"),
    [
        (Path(".vibetable/workspace.json"), "storageMode", "direct"),
        (Path(".vibetable/workspace.json"), "formatVersion", 1),
        (REMOTE / "identity.json", "workspaceId", "33333333-3333-4333-8333-333333333333"),
        (REMOTE / "identity.json", "strength", "strong"),
        (REMOTE / "identity.json", "replicaId", "not-a-uuid"),
    ],
)
def test_seed_rejects_wrong_directory_identity_before_writing(
    tmp_path: Path, identity_file: Path, field: str, value: object
) -> None:
    source = _replica(tmp_path / "left")
    identity_path = source / identity_file
    identity = json.loads(identity_path.read_text(encoding="utf-8"))
    identity[field] = value
    identity_path.write_text(json.dumps(identity), encoding="utf-8")

    with pytest.raises(ValueError, match=r"identity|mirrored|UUID"):
        seed_replica_payloads(source, tmp_path / "right")

    assert not (tmp_path / "right").exists()


def test_failed_copy_keeps_source_and_failure_evidence_without_completing_exchange(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    left, right = _replica(tmp_path / "left"), _replica(tmp_path / "right")
    _write(left, REMOTE / "publications/left.json", b"left publication")
    _write(right, REMOTE / "objects/right.blob", b"right object")
    failing_target = left / REMOTE / "objects/right.blob"
    original = shutil.copyfileobj

    def copy(source: BinaryIO, target: BinaryIO, length: int = 0) -> None:
        if Path(target.name) == failing_target:
            target.write(b"partial")
            raise OSError("disk full")
        original(source, target, length)

    monkeypatch.setattr(shutil, "copyfileobj", copy)

    with pytest.raises(OSError, match="disk full"):
        exchange_replica_payloads(left, right)

    assert failing_target.read_bytes() == b"partial"
    assert (right / REMOTE / "objects/right.blob").read_bytes() == b"right object"
    assert not (right / REMOTE / "publications/left.json").exists()


def test_different_replica_identity_cannot_be_merged(tmp_path: Path) -> None:
    left, right = _replica(tmp_path / "left"), _replica(tmp_path / "right")
    identity_path = right / REMOTE / "identity.json"
    identity = json.loads(identity_path.read_text(encoding="utf-8"))
    identity["replicaId"] = "33333333-3333-4333-8333-333333333333"
    identity_path.write_text(json.dumps(identity), encoding="utf-8")
    _write(right, REMOTE / "publications/right.json", b"right publication")

    with pytest.raises(ValueError, match=r"immutable payload differs.*identity\.json"):
        exchange_replica_payloads(left, right)

    assert not (left / REMOTE / "publications/right.json").exists()


@pytest.mark.parametrize("relationship", ["same", "nested"])
def test_exchange_refuses_overlapping_selected_roots(tmp_path: Path, relationship: str) -> None:
    left = _replica(tmp_path / "left")
    right = left if relationship == "same" else _replica(left / "nested")

    with pytest.raises(ValueError, match="overlap"):
        exchange_replica_payloads(left, right)
