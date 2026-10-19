"""Offline synthetic legacy records: called only after the owned Host has exited."""

from __future__ import annotations

import json
import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Any

COLLECTIONS = ("vibetable_interfaces", "vibetable_dashboards", "vibetable_panels")


def seed_legacy_layouts(workspace: Path, approved_root: Path, seed: dict[str, Any]) -> None:
    if not workspace.is_absolute() or not approved_root.is_absolute():
        raise ValueError("absolute test paths required")
    workspace = workspace.resolve(strict=True)
    approved_root = approved_root.resolve(strict=True)
    if not workspace.is_relative_to(approved_root) or workspace == approved_root:
        raise ValueError("workspace must be inside this test run")
    manifest = json.loads((workspace / ".vibetable/workspace.json").read_text(encoding="utf-8"))
    if manifest.get("workspaceId") != seed["workspaceId"]:
        raise ValueError("synthetic workspace identity mismatch")
    if seed.get("lifecycle", {}).get("status") != "passed":
        raise ValueError("owned Host exit must be proved before offline seeding")
    table_id, field_id = seed["tableId"], seed["field"]["fieldId"]
    definition = {
        "contractVersion": "1.0",
        "interfaceId": "legacy-records",
        "name": "Historical records",
        "bindings": [
            {
                "bindingId": "records",
                "query": {
                    "contractVersion": "1.0",
                    "tableId": table_id,
                    "fields": [field_id],
                    "filters": [],
                    "sorts": [],
                    "cursor": None,
                    "pageSize": 2,
                },
                "variables": [],
            }
        ],
        "actions": [
            {
                "actionId": "create",
                "kind": "record.create",
                "bindingId": "records",
                "requiresConfirmation": True,
            }
        ],
        "pages": [
            {
                "pageId": "main",
                "title": "Records",
                "elements": [
                    {"elementId": "list", "kind": "record-list", "bindingId": "records"},
                    {"elementId": "form", "kind": "form", "bindingId": "records"},
                ],
            }
        ],
    }
    complex_definition = {
        "contractVersion": "future",
        "interfaceId": "legacy-complex",
        "name": "Complex",
        "bindings": [],
        "actions": [{"actionId": "old", "kind": "plugin", "pluginId": "old.worker"}],
        "pages": [
            {
                "pageId": "main",
                "elements": [
                    {
                        "elementId": "section",
                        "kind": "section",
                        "children": [
                            {
                                "elementId": "unknown",
                                "kind": "future-chart",
                                "unknownExpression": "never evaluate",
                            }
                        ],
                    }
                ],
            }
        ],
        "unknownField": {"preserve": True},
    }
    records = [
        (
            COLLECTIONS[0],
            "legacyrecord001",
            "legacy-records",
            "",
            json.dumps(definition, ensure_ascii=False),
        ),
        (
            COLLECTIONS[0],
            "legacyrecord002",
            "legacy-complex",
            "",
            json.dumps(complex_definition, ensure_ascii=False),
        ),
        (
            COLLECTIONS[0],
            "legacyrecord003",
            "legacy-broken",
            "",
            "{broken JSON with original whitespace  ",
        ),
        (
            COLLECTIONS[1],
            "legacyrecord004",
            "legacy-dashboard",
            "",
            '{"id":"legacy-dashboard","name":"Historical dashboard","configVersion":1}',
        ),
        (
            COLLECTIONS[2],
            "legacyrecord005",
            "legacy-panel",
            "legacy-dashboard",
            json.dumps(
                {
                    "id": "legacy-panel",
                    "dashboardId": "legacy-dashboard",
                    "name": "Metric",
                    "type": "metric",
                    "query": {"tableId": table_id},
                    "options": {},
                }
            ),
        ),
    ]
    database = workspace / ".vibetable/data/data.db"
    with (
        closing(sqlite3.connect(f"{database.as_uri()}?mode=rw", uri=True)) as connection,
        connection,
    ):
        for collection in COLLECTIONS:
            if connection.execute(f'SELECT COUNT(*) FROM "{collection}"').fetchone()[0]:
                raise ValueError("synthetic legacy collections must start empty")
        for collection, record_id, logical_id, parent, payload in records:
            columns, values = ["id", "logical_id", "payload_json"], [record_id, logical_id, payload]
            if collection == COLLECTIONS[2]:
                columns.append("parent_id")
                values.append(parent)
            connection.execute(
                f'INSERT INTO "{collection}" ({", ".join(columns)}) VALUES ({", ".join("?" for _ in values)})',
                values,
            )


def preservation_proof(
    connection: sqlite3.Connection, physical_name: str, table_id: str
) -> dict[str, Any]:
    """Semantic evidence, including exact original payload text and business identities."""

    def rows(table: str, where: str = "", params: tuple[str, ...] = ()) -> list[dict[str, Any]]:
        cursor = connection.execute(f'SELECT * FROM "{table}" {where} ORDER BY id', params)
        names = [column[0] for column in cursor.description]
        return [dict(zip(names, row, strict=True)) for row in cursor.fetchall()]

    evidence = {collection: rows(collection) for collection in COLLECTIONS}
    evidence["businessRecords"] = rows(physical_name)
    for table in ("vibetable_tables", "vibetable_fields", "vibetable_audit_events"):
        evidence[table] = rows(table, "WHERE table_id = ?", (table_id,))
    tables = {
        row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")
    }
    for table in sorted(tables):
        if table.startswith(
            ("vibetable_attachment", "vibetable_file_revision", "vibetable_documents")
        ):
            evidence[table] = rows(table)
    return evidence
