from __future__ import annotations

import ast
from pathlib import Path

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
    assert matrix.PUBLISH_ROOT == matrix.REPO_ROOT / "dist" / "VibeTable.Next"
    assert "scripts/build_next.py" in source
    assert 'command.append("--release")' in source
    assert "NewWithConfig" not in source


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
        "backup+restore",
        "record-delete",
    }
    assert all(f'coverage["{axis}"] = "passed"' in source for axis in expected)
