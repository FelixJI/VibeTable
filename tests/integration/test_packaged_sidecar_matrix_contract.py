from __future__ import annotations

import ast
import json
import os
import subprocess
from pathlib import Path

import pytest

from tests.integration import packaged_sidecar_matrix as matrix


def test_sidecar_start_failure_stops_process_and_closes_pipes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = Path(os.environ["SYSTEMROOT"]) / "System32" / "where.exe"
    data_dir = tmp_path / "data"
    data_dir.mkdir()
    real_popen = subprocess.Popen
    started: list[subprocess.Popen[str]] = []

    def capture_popen(
        args: list[str],
        *,
        cwd: Path,
        env: dict[str, str],
        stdout: int,
        stderr: int,
        text: bool,
        encoding: str,
        errors: str,
    ) -> subprocess.Popen[str]:
        process = real_popen(
            args,
            cwd=cwd,
            env=env,
            stdout=stdout,
            stderr=stderr,
            text=text,
            encoding=encoding,
            errors=errors,
        )
        started.append(process)
        return process

    monkeypatch.setattr(matrix.subprocess, "Popen", capture_popen)
    sidecar = matrix.Sidecar(binary, data_dir)

    with pytest.raises(AssertionError, match="invalid readiness line"):
        sidecar.start()

    assert len(started) == 1
    process = started[0]
    assert process.poll() is not None
    assert process.stdout is not None
    assert process.stdout.closed
    assert process.stderr is not None
    assert process.stderr.closed
    assert sidecar.process is None
    assert sidecar.address == ""
    data_dir.rmdir()


def test_strict_entry_builds_release_and_uses_only_published_binary() -> None:
    source = Path(matrix.__file__).read_text(encoding="utf-8")
    tree = ast.parse(source)
    imports = {
        alias.name
        for node in ast.walk(tree)
        if isinstance(node, ast.Import)
        for alias in node.names
    }
    assert "pocketbase" not in imports
    assert matrix.PUBLISH_ROOT == (matrix.REPO_ROOT / "dist" / "VibeTable.Next")
    assert "scripts/build_next.py" in source
    assert 'command.append("--release")' in source
    assert "NewWithConfig" not in source


def test_matrix_resolves_sidecar_from_publish_layout(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    binary = package_root / "resources" / "sidecar" / matrix.SIDECAR_NAME
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"sidecar")
    layout = package_root / "resources" / "publish-layout.json"
    layout.write_text(
        json.dumps({"launch": {"sidecar": f"resources/sidecar/{matrix.SIDECAR_NAME}"}}),
        encoding="utf-8",
    )

    assert matrix.published_sidecar_binary(package_root) == binary.resolve()


def test_matrix_rejects_sidecar_path_outside_package_root(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    resources = package_root / "resources"
    resources.mkdir(parents=True)
    (resources / "publish-layout.json").write_text(
        json.dumps({"launch": {"sidecar": f"../{matrix.SIDECAR_NAME}"}}),
        encoding="utf-8",
    )

    with pytest.raises(AssertionError, match="escapes package root"):
        matrix.published_sidecar_binary(package_root)


def test_matrix_declares_every_plan_12_4_coverage_axis() -> None:
    source = Path(matrix.__file__).read_text(encoding="utf-8")
    expected = {
        "fresh-data+migrations",
        "schema-create-alter-index",
        "record-crud-query+relation-lookup",
        "atomic-batch-rollback",
        "formula-preview-save-backfill",
        "file-upload-download-delete-thumb-protected",
        "audit+restore",
        "sse",
        "process-restart",
        "record-delete",
        "workspace-v2-build-info",
        "workspace-v2-capabilities",
        "workspace-v2-legacy-write-rejection",
        "workspace-v2-snapshot-package",
    }
    assert all(axis in source for axis in expected)
    assert '"backup+restore"' not in source
