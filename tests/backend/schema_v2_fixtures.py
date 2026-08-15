from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from backend.contracts.schema_v2 import FieldDefinitionV2, SchemaSnapshotV2

_FIELD_FIXTURE = (
    Path(__file__).parents[2] / "contracts" / "schema-v2" / "fixtures" / "field-definition.json"
)


def field_v2(
    name: str,
    logical_type: str = "text",
    *,
    display_name: str | None = None,
    required: bool = False,
    writable: bool = True,
    unique: bool = False,
    target_table_id: str = "customers",
) -> dict[str, Any]:
    payload = json.loads(_FIELD_FIXTURE.read_text(encoding="utf-8"))
    token = ("".join(character for character in name.lower() if character.isalnum()) + "00000000")[
        :8
    ]
    payload["identity"] = {
        "fieldId": f"fld_{token}",
        "physicalName": f"f_{token}",
        "providerFieldId": f"pb_{token}",
    }
    payload["displayName"] = display_name or name.title()
    payload["logicalType"] = logical_type
    payload["lifecycle"] = {
        "state": "active" if writable else "retired",
        "retiredAt": None if writable else "2026-08-13T00:00:00Z",
    }
    payload["value"]["required"] = required
    payload["constraints"]["unique"]["enabled"] = unique
    payload["storage"]["kind"] = {
        "text": "pocketbase-text",
        "editor": "pocketbase-editor",
        "number": "pocketbase-number",
        "select": "pocketbase-select",
        "multiSelect": "pocketbase-select",
        "relation": "pocketbase-relation",
        "json": "pocketbase-json",
        "autoDate": "pocketbase-autodate",
        "formula": "computed",
    }[logical_type]
    payload["display"]["kind"] = {
        "text": "text",
        "editor": "editor",
        "number": "number",
        "select": "select",
        "multiSelect": "select",
        "relation": "relation",
        "json": "json",
        "autoDate": "readonly",
        "formula": "readonly",
    }[logical_type]
    for key in ("select", "relation", "file", "json", "autoDate", "formula", "lookup"):
        payload.pop(key, None)
    if logical_type in {"select", "multiSelect"}:
        payload["select"] = {
            "options": [
                {
                    "optionId": "opt_active000",
                    "label": "Active",
                    "color": "green",
                    "order": 0,
                    "state": "active",
                }
            ]
        }
    elif logical_type == "relation":
        payload["relation"] = {
            "targetTableId": target_table_id,
            "cardinality": "one",
            "deletePolicy": "setNull",
            "displayFieldId": "fld_display0",
        }
    elif logical_type == "json":
        payload["json"] = {"rootType": "object", "maxSize": 1024, "schema": {}}
    elif logical_type == "formula":
        payload["value"]["presence"] = {"mode": "computed"}
        payload["formula"] = {
            "language": "cel-v1",
            "source": "1 + 1",
            "resultType": "number",
        }
    elif logical_type == "autoDate":
        payload["value"]["presence"] = {"mode": "computed"}
        payload["autoDate"] = {"role": "createdAt"}
    return FieldDefinitionV2.model_validate(payload).model_dump(by_alias=True, mode="json")


def snapshot_v2(
    table_id: str,
    fields: list[dict[str, Any]],
    *,
    revision: str,
    archive_field: str | None = None,
) -> dict[str, Any]:
    payload = {
        "contract": "vibetable.schema.v2",
        "tableId": table_id,
        "displayName": table_id.title(),
        "kind": "base",
        "schemaRevision": revision,
        "dataRevision": 1,
        "archivePolicy": {
            "mode": "status" if archive_field else "none",
            "fieldId": archive_field,
            "archivedValue": "archived" if archive_field else None,
        },
        "fields": fields,
        "capabilities": [],
    }
    return SchemaSnapshotV2.model_validate(payload).model_dump(by_alias=True, mode="json")
