from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.data_io import (
    PocketBasePasteReadPort,
    collection_profile_from_definition,
)
from backend.application.paste_service import PasteError


def _definition(*, revision: str = "schema_7", readonly: bool = False) -> dict[str, Any]:
    return {
        "contractVersion": "2.0",
        "tableId": "orders",
        "physicalName": "orders",
        "displayName": "Orders",
        "kind": "base",
        "schemaRevision": revision,
        "archivePolicy": {
            "mode": "status",
            "fieldId": "status",
            "archivedValue": "archived",
        },
        "fields": [
            {
                "fieldId": "title",
                "physicalName": "title",
                "displayName": "Title",
                "kind": "scalar",
                "dataType": "shortText",
                "nullable": False,
                "constraints": [],
                "readOnly": readonly,
            },
            {
                "fieldId": "amount",
                "physicalName": "amount",
                "displayName": "Amount",
                "kind": "scalar",
                "dataType": "decimal",
                "nullable": True,
                "constraints": [{"kind": "precisionScale", "precision": 12, "scale": 2}],
                "readOnly": False,
            },
            {
                "fieldId": "status",
                "physicalName": "status",
                "displayName": "Status",
                "kind": "scalar",
                "dataType": "select",
                "nullable": False,
                "constraints": [],
                "readOnly": False,
            },
            {
                "fieldId": "total",
                "physicalName": "total",
                "displayName": "Total",
                "kind": "formula",
                "dataType": "formula",
                "nullable": True,
                "constraints": [],
                "readOnly": True,
            },
        ],
        "indexes": [],
    }


def test_profile_projects_live_schema_revision_and_writable_fields() -> None:
    profile = collection_profile_from_definition(_definition())

    assert profile.capability_hash == "schema_7"
    assert profile.fields == ["id", "title", "amount", "status", "total"]
    assert profile.create_fields == ["title", "amount", "status"]
    assert profile.update_fields == ["title", "amount", "status"]
    assert profile.archive_field == "status"
    assert profile.date_updated_field is None


class _Client:
    def __init__(self, definition: dict[str, Any]) -> None:
        self.definition = definition

    async def describe_table(self, _table_id: str) -> dict[str, Any]:
        return self.definition

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[dict[str, Any]]:
        assert table_id == "orders"
        return [
            {
                "id": row_ids[0],
                "title": "before",
                "__vibetableDigest": "sha256:" + "a" * 64,
            }
        ]


@pytest.mark.asyncio
async def test_paste_read_port_refreshes_policy_and_exposes_decimal_shape() -> None:
    original = _definition()
    profile = collection_profile_from_definition(original)
    profiles = {"orders": profile}
    definitions = {"orders": original}
    client = _Client(_definition(readonly=True))
    port = PocketBasePasteReadPort(
        client=client,  # type: ignore[arg-type]
        profiles=profiles,
        definitions=definitions,
    )

    fields = await port.fields(profile)
    amount = next(item for item in fields if item["field"] == "amount")
    assert amount["schema"] == {
        "data_type": "decimal",
        "numeric_scale": 2,
        "numeric_precision": 12,
    }
    title = next(item for item in fields if item["field"] == "title")
    assert title["schema"]["data_type"] == "shortText"
    row = await port.read_item(profile, "row-1")
    assert row["__vibetableDigest"] == "sha256:" + "a" * 64
    with pytest.raises(PasteError, match="field policy changed"):
        await port.require_write_fields(
            profile,
            {"title"},
            operation="update",
            refresh=True,
        )
