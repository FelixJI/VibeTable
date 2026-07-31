from __future__ import annotations

import ast
import json
from pathlib import Path

import pytest

from tests.integration import packaged_sidecar_matrix as matrix


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
