"""The immutable Surface corpus is independently replayed from reachable Python source."""

from __future__ import annotations

import copy
import io
import json
import subprocess
import tarfile
from pathlib import Path

import pytest

from contracts.v2 import generate_surface_python_oracle as oracle_tool
from contracts.v2.generate_surface_python_oracle import (
    ORACLE,
    extract_backend,
    replay_producer,
    verify_capture,
)


def test_fixed_python_replay_matches_all_samples_and_rejects_tampering(tmp_path: Path) -> None:
    captured = replay_producer()
    verify_capture(captured)
    current = json.loads(ORACLE.read_text(encoding="utf-8"))
    assert len(current["cases"]) == 78
    assert sum(len(sample["calls"]) for sample in current["cases"]) == 81
    for field, replacement in (
        ("responses", [{"result": "tampered"}]),
        ("metadataEffects", {"writes": False, "deletes": 0}),
    ):
        changed = copy.deepcopy(current)
        changed["cases"][0][field] = replacement
        path = tmp_path / f"tampered-{field}.json"
        path.write_text(json.dumps(changed), encoding="utf-8")
        with pytest.raises(ValueError, match="differs from the fixed Python producer replay"):
            verify_capture(captured, path)


@pytest.mark.parametrize(
    ("name", "kind"),
    [
        ("backend/../../outside.py", tarfile.REGTYPE),
        ("backend\\outside.py", tarfile.REGTYPE),
        ("/backend/outside.py", tarfile.REGTYPE),
        ("other/file.py", tarfile.REGTYPE),
        ("backend/file.py:stream", tarfile.REGTYPE),
        ("backend/link", tarfile.SYMTYPE),
        ("backend/link", tarfile.LNKTYPE),
        ("backend/pipe", tarfile.FIFOTYPE),
    ],
)
def test_archive_rejects_non_backend_paths_and_special_members(
    tmp_path: Path, name: str, kind: bytes
) -> None:
    payload = io.BytesIO()
    with tarfile.open(fileobj=payload, mode="w") as archive:
        member = tarfile.TarInfo(name)
        member.type = kind
        archive.addfile(member)
    destination = tmp_path / "producer"
    with pytest.raises(ValueError, match="Unsafe Python producer archive member"):
        extract_backend(payload.getvalue(), destination)
    assert not destination.exists()


def test_missing_fixed_producer_fails_closed(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(oracle_tool, "PRODUCER", "0" * 40)
    with pytest.raises(subprocess.CalledProcessError):
        oracle_tool.replay_producer()
