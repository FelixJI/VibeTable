"""Transport opaque, product-created replica payloads between stopped E2E hosts.

Callers own host shutdown. This helper never copies local authority or user files;
the product's existing replica reader remains responsible for payload validity.
"""

from __future__ import annotations

import json
import shutil
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path
from uuid import UUID


@dataclass(frozen=True)
class ReplicaPayloadExchange:
    workspace_id: str
    added_to_left: tuple[str, ...]
    added_to_right: tuple[str, ...]


@dataclass(frozen=True)
class _Payload:
    workspace_id: str
    files: dict[Path, Path]


def _reject_link(path: Path) -> None:
    if path.is_symlink() or path.is_junction():
        raise ValueError(f"linked replica payload is not supported: {path}")


def _files_under(directory: Path) -> Iterator[Path]:
    _reject_link(directory)
    if not directory.exists():
        return
    for path in sorted(directory.iterdir()):
        _reject_link(path)
        if path.is_dir():
            yield from _files_under(path)
        elif path.is_file():
            if path.name.startswith(".") and path.name.endswith(".tmp"):
                continue
            yield path
        else:
            raise ValueError(f"replica payload is not a regular file: {path}")


def _identity(path: Path, version: int) -> dict[str, object]:
    _reject_link(path)
    value: object = json.loads(path.read_text(encoding="utf-8"))
    if (
        not isinstance(value, dict)
        or type(value.get("formatVersion")) is not int
        or value.get("formatVersion") != version
    ):
        raise ValueError(f"unsupported replica directory identity: {path}")
    return value


def _canonical_uuid(value: object) -> str:
    if not isinstance(value, str):
        raise ValueError("replica directory identity requires a UUID")
    parsed = UUID(value)
    if not parsed.int or str(parsed) != value:
        raise ValueError("replica directory identity requires a canonical UUID")
    return value


def _payload(root: Path) -> _Payload:
    _reject_link(root)
    _reject_link(root / ".vibetable")
    manifest_path = root / ".vibetable/workspace.json"
    manifest = _identity(manifest_path, 2)
    if manifest.get("storageMode") != "mirrored":
        raise ValueError("replica seed requires a mirrored workspace")
    workspace_id = _canonical_uuid(manifest.get("workspaceId"))
    home = root / ".vibetable/replica-v2" / workspace_id
    _reject_link(home.parent)
    _reject_link(home)
    identity_path = home / "identity.json"
    identity = _identity(identity_path, 1)
    if identity.get("workspaceId") != workspace_id or identity.get("strength") != "advisory":
        raise ValueError("replica directory identity does not match the mirrored workspace")
    _canonical_uuid(identity.get("replicaId"))
    files = {
        manifest_path.relative_to(root): manifest_path,
        identity_path.relative_to(root): identity_path,
    }
    for directory in ("publications", "checkpoints", "objects", "manifests"):
        for path in _files_under(home / directory):
            files[path.relative_to(root)] = path
    return _Payload(workspace_id, files)


def _copy_files(files: dict[Path, Path], destination: Path) -> tuple[str, ...]:
    copied: list[str] = []
    for relative, source in sorted(files.items()):
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        with source.open("rb") as incoming, target.open("xb") as outgoing:
            shutil.copyfileobj(incoming, outgoing)
        copied.append(relative.as_posix())
    return tuple(copied)


def _check_distinct_roots(left: Path, right: Path) -> None:
    _reject_link(left)
    _reject_link(right)
    source_root, destination_root = left.resolve(), right.resolve()
    if (
        source_root == destination_root
        or source_root in destination_root.parents
        or destination_root in source_root.parents
    ):
        raise ValueError("replica roots overlap")


def seed_replica_payloads(source: Path, destination: Path) -> tuple[str, ...]:
    """Copy a common published seed, without initializing or editing its contents."""
    _check_distinct_roots(source, destination)
    if destination.exists() and (not destination.is_dir() or any(destination.iterdir())):
        raise ValueError("replica seed destination must be empty")
    payload = _payload(source)
    destination.mkdir(parents=True, exist_ok=True)
    return _copy_files(payload.files, destination)


def _same_content(left: Path, right: Path) -> bool:
    with left.open("rb") as a, right.open("rb") as b:
        while chunk := a.read(64 * 1024):
            if chunk != b.read(len(chunk)):
                return False
        return b.read(1) == b""


def exchange_replica_payloads(left: Path, right: Path) -> ReplicaPayloadExchange:
    """Append missing immutable artifacts to each side; never overwrite or delete.

    Preflight rejects conflicting existing artifacts before either side changes.
    I/O failures propagate and retain partial files as failed-run evidence; callers
    must not reopen hosts or treat a partial exchange as successful.
    """
    _check_distinct_roots(left, right)
    left_payload, right_payload = _payload(left), _payload(right)
    for relative in left_payload.files.keys() & right_payload.files.keys():
        if not _same_content(left_payload.files[relative], right_payload.files[relative]):
            raise ValueError(f"immutable payload differs: {relative.as_posix()}")
    to_left = {
        path: source
        for path, source in right_payload.files.items()
        if path not in left_payload.files
    }
    to_right = {
        path: source
        for path, source in left_payload.files.items()
        if path not in right_payload.files
    }
    return ReplicaPayloadExchange(
        left_payload.workspace_id,
        _copy_files(to_left, left),
        _copy_files(to_right, right),
    )
