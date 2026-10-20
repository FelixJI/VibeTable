"""The fixed platform mode accepts only framed ZIP bytes and cannot execute plugins."""

from __future__ import annotations

import base64
import io
import json
import sys
from pathlib import Path

import pytest

from backend.infrastructure.plugin_package import PackagePolicy, _normalized_package_digest
from backend.infrastructure.plugin_package_verifier import READY, run

FIXTURES = (
    Path(__file__).resolve().parents[3]
    / "desktop/tests/VibeTable.Desktop.Tests/Fixtures/plugin-preservation-compression.json"
)


def _invoke(data: bytes) -> tuple[int, bytes, bytes]:
    output, error = io.BytesIO(), io.BytesIO()
    status = run(io.BytesIO(data), output, error)
    return status, output.getvalue(), error.getvalue()


@pytest.mark.parametrize("name", json.loads(FIXTURES.read_text(encoding="utf-8"))["cases"])
def test_fixed_authority_compression_and_mismatched_local_headers(name: str) -> None:
    case = json.loads(FIXTURES.read_text(encoding="utf-8"))["cases"][name]
    data = base64.b64decode(case["archive"])
    status, output, error = _invoke(len(data).to_bytes(8, "big") + data)
    assert output.startswith(READY)
    if case["authorityAccepted"]:
        assert (status, output, error) == (0, READY + case["packageHash"].encode() + b"\n", b"")
    elif name.startswith("corrupt-"):
        # Parsing succeeds, but the Host rejects the returned identity against its
        # installed authority; preservation does not impose new integrity rules.
        assert status == 0
        assert output != READY + case["packageHash"].encode() + b"\n"
        assert error == b""
    else:
        assert status == 1
        assert output == READY
        assert len(error) < 128


@pytest.mark.parametrize(
    "data",
    [
        b"",
        b"short",
        (PackagePolicy.MAX_PACKAGE_BYTES + 1).to_bytes(8, "big"),
        (1).to_bytes(8, "big"),
        (0).to_bytes(8, "big") + b"extra",
    ],
)
def test_frame_size_eof_and_zip_fail_closed(data: bytes) -> None:
    status, output, error = _invoke(data)
    assert status == 1
    assert output == READY
    assert error == b"Retained package verification failed.\n"


def test_legacy_worker_and_python_members_are_only_hashed(tmp_path: Path) -> None:
    import zipfile

    marker = tmp_path / "executed"
    entries = [
        (
            "manifest.json",
            b'{"$schema":"vibetable.plugin-manifest.v1","actions":'
            b'[{"workerEntry":"worker.py"}],"unknownOriginalField":true}',
        ),
        ("worker.py", f"from pathlib import Path; Path({str(marker)!r}).touch()".encode()),
        ("__main__.py", b"raise RuntimeError('plugin must never execute')"),
    ]
    data = io.BytesIO()
    with zipfile.ZipFile(data, "w") as archive:
        for name, content in entries:
            archive.writestr(name, content)
    raw = data.getvalue()
    status, output, error = _invoke(len(raw).to_bytes(8, "big") + raw)
    assert (status, output, error) == (
        0,
        READY + _normalized_package_digest(entries).encode() + b"\n",
        b"",
    )
    assert not marker.exists()


def test_fixed_main_mode_never_builds_a_server(monkeypatch: pytest.MonkeyPatch) -> None:
    from backend import __main__ as entry
    from backend.infrastructure import plugin_package_verifier

    def forbidden() -> None:
        raise AssertionError("the fixed platform mode must not compose a server")

    called = []

    def verify(source: object, output: object, error: object) -> int:
        called.append((source, output, error))
        return 0

    monkeypatch.setattr(entry, "_build_server", forbidden)
    monkeypatch.setattr(entry, "_product_runtime", forbidden)
    monkeypatch.setattr(entry, "_main", forbidden)
    monkeypatch.setattr(plugin_package_verifier, "run", verify)
    monkeypatch.setattr(sys, "argv", ["backend", "--verify-retained-plugin-package"])
    with pytest.raises(SystemExit) as result:
        entry.main()
    assert result.value.code == 0
    assert len(called) == 1


def test_fixed_main_rejects_extra_arguments(monkeypatch: pytest.MonkeyPatch) -> None:
    from backend import __main__ as entry

    monkeypatch.setattr(sys, "argv", ["backend", "--verify-retained-plugin-package", "plugin-path"])
    with pytest.raises(SystemExit, match="unsupported backend mode"):
        entry.main()
