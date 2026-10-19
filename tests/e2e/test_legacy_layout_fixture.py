import json
import sqlite3
from contextlib import closing
from pathlib import Path

import pytest

from tests.e2e.legacy_layout_fixture import COLLECTIONS, seed_legacy_layouts


def test_offline_seed_is_identity_bound_preserves_broken_bytes_and_refuses_repeat(
    tmp_path: Path,
) -> None:
    workspace = tmp_path / "workspace"
    data = workspace / ".vibetable/data"
    data.mkdir(parents=True)
    (workspace / ".vibetable/workspace.json").write_text(
        json.dumps({"workspaceId": "test-workspace"}), encoding="utf-8"
    )
    database = data / "data.db"
    with closing(sqlite3.connect(database)) as connection, connection:
        for collection in COLLECTIONS:
            connection.execute(
                f'CREATE TABLE "{collection}" (id TEXT PRIMARY KEY, logical_id TEXT, payload_json TEXT, parent_id TEXT)'
            )
    seed = {
        "workspaceId": "test-workspace",
        "tableId": "tbl-existing",
        "field": {"fieldId": "fld-existing"},
        "lifecycle": {"status": "passed"},
    }
    with pytest.raises(ValueError, match="identity"):
        seed_legacy_layouts(workspace, tmp_path, seed | {"workspaceId": "other"})
    with pytest.raises(ValueError, match="Host exit"):
        seed_legacy_layouts(workspace, tmp_path, seed | {"lifecycle": {"status": "failed"}})
    with pytest.raises(ValueError, match="inside"):
        seed_legacy_layouts(workspace, workspace, seed)
    seed_legacy_layouts(workspace, tmp_path, seed)
    with closing(sqlite3.connect(database)) as connection, connection:
        assert connection.execute("SELECT COUNT(*) FROM vibetable_interfaces").fetchone()[0] == 3
        assert (
            connection.execute(
                "SELECT payload_json FROM vibetable_interfaces WHERE logical_id='legacy-broken'"
            ).fetchone()[0]
            == "{broken JSON with original whitespace  "
        )
        assert (
            connection.execute("SELECT parent_id FROM vibetable_panels").fetchone()[0]
            == "legacy-dashboard"
        )
    with pytest.raises(ValueError, match="start empty"):
        seed_legacy_layouts(workspace, tmp_path, seed)
