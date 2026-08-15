from __future__ import annotations

import pytest

from backend.adapters.pocketbase.data_io import (
    PocketBasePasteReadPort,
    collection_profile_from_definition,
)
from backend.application.paste_service import PasteError
from backend.application.revisioned_metadata_port import JsonObject, json_object
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2


def _definition(*, revision: str = "schema_7", readonly: bool = False) -> JsonObject:
    fields = [
        field_v2("title", required=True, writable=not readonly),
        field_v2("amount", "number"),
        field_v2("status", "select", required=True),
        field_v2("total", "formula", writable=False),
    ]
    return json_object(
        snapshot_v2("orders", fields, revision=revision, archive_field="fld_status00")
    )


def test_profile_projects_live_schema_revision_and_writable_fields() -> None:
    profile = collection_profile_from_definition(_definition())

    assert profile.capability_hash == "schema_7"
    assert profile.fields == ["id", "f_title000", "f_amount00", "f_status00", "f_total000"]
    assert profile.create_fields == ["f_title000", "f_amount00", "f_status00"]
    assert profile.update_fields == ["f_title000", "f_amount00", "f_status00"]
    assert profile.archive_field == "f_status00"
    assert profile.date_updated_field is None


class _Client:
    def __init__(self, definition: JsonObject) -> None:
        self.definition = definition

    async def describe_table(self, table_id: str) -> JsonObject:
        assert table_id == "orders"
        return self.definition

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[JsonObject]:
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
        client=client,
        profiles=profiles,
        definitions=definitions,
    )

    fields = await port.fields(profile)
    amount = next(item for item in fields if item["field"] == "f_amount00")
    assert amount["schema"] == {
        "data_type": "number",
        "numeric_scale": 2,
        "numeric_precision": None,
    }
    title = next(item for item in fields if item["field"] == "f_title000")
    title_schema = title["schema"]
    assert isinstance(title_schema, dict)
    assert title_schema["data_type"] == "text"
    row = await port.read_item(profile, "row-1")
    assert row["__vibetableDigest"] == "sha256:" + "a" * 64
    with pytest.raises(PasteError, match="field policy changed"):
        await port.require_write_fields(
            profile,
            {"f_title000"},
            operation="update",
            refresh=True,
        )


@pytest.mark.asyncio
async def test_paste_read_port_rejects_non_finite_schema_field_metadata() -> None:
    definition = _definition()
    raw_fields = definition["fields"]
    assert isinstance(raw_fields, list)
    first_field = raw_fields[0]
    assert isinstance(first_field, dict)
    first_field["storage"] = {"options": {"scale": float("nan")}}
    profile = collection_profile_from_definition(_definition())
    port = PocketBasePasteReadPort(
        client=_Client(_definition()),
        profiles={"orders": profile},
        definitions={"orders": definition},
    )

    with pytest.raises(ValueError, match="numbers must be finite"):
        await port.fields(profile)
