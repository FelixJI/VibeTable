from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.plugin_mutation import PocketBasePluginMutationAdapter
from backend.contracts.plugin import MutationPlan


class FakeClient:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []
        self.definitions: list[dict[str, Any]] = []
        self.receipt_status = "applied"

    async def apply_mutation(self, request: dict[str, Any]) -> dict[str, Any]:
        self.requests.append(request)
        return {"status": self.receipt_status, "affectedRows": [{"recordId": "1"}]}

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        assert table_id == "orders"
        return self.definitions.pop(0)


def _plan(*, collection: str = "orders", values: dict[str, Any] | None = None) -> MutationPlan:
    return MutationPlan.model_validate(
        {
            "contract": "vibetable.mutation-plan.v1",
            "collection": collection,
            "operations": [
                {
                    "kind": "update",
                    "primaryKey": "1",
                    "values": values or {"status": "done"},
                }
            ],
            "preview": {"affectedCount": 1},
            "idempotencyKey": "plugin-run-1",
        }
    )


@pytest.mark.asyncio
async def test_plugin_plan_is_translated_to_mutation_kernel_contract() -> None:
    client = FakeClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status", "note"}},
    )

    result = await adapter.apply(_plan())

    assert result["contract"] == "vibetable.plugin-result.v1"
    assert result["status"] == "success"
    request = client.requests[0]
    assert request["tableId"] == "orders"
    assert request["schemaRevision"] == "schema-7"
    assert request["operations"] == [
        {"kind": "update", "recordId": "1", "values": {"status": "done"}}
    ]
    assert "url" not in repr(request).lower()
    assert "token" not in repr(request).lower()


@pytest.mark.asyncio
async def test_plugin_plan_rejects_ungranted_collection_before_io() -> None:
    client = FakeClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )

    with pytest.raises(ValueError, match="collection"):
        await adapter.apply(_plan(collection="secrets"))

    assert client.requests == []


@pytest.mark.asyncio
async def test_plugin_plan_rejects_ungranted_field_before_io() -> None:
    client = FakeClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )

    with pytest.raises(ValueError, match="field"):
        await adapter.apply(_plan(values={"admin": True}))

    assert client.requests == []


@pytest.mark.asyncio
async def test_plugin_plan_accepts_idempotent_replay_receipt() -> None:
    client = FakeClient()
    client.receipt_status = "replayed"
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )

    result = await adapter.apply(_plan())

    assert result["status"] == "success"
    assert result["metrics"] == [{"label": "affectedRows", "value": 1}]


@pytest.mark.asyncio
async def test_dynamic_plugin_grant_refreshes_schema_before_every_plan() -> None:
    client = FakeClient()
    client.definitions = [
        {
            "tableId": "orders",
            "schemaRevision": "schema-7",
            "fields": [
                {
                    "fieldId": "fld_status",
                    "physicalName": "status",
                    "kind": "text",
                    "dataType": "text",
                    "nullable": True,
                    "constraints": [],
                },
            ],
        },
        {
            "tableId": "orders",
            "schemaRevision": "schema-8",
            "fields": [
                {
                    "fieldId": "fld_status",
                    "physicalName": "status",
                    "kind": "text",
                    "dataType": "text",
                    "nullable": True,
                    "constraints": [],
                },
                {
                    "fieldId": "fld_note",
                    "physicalName": "note",
                    "kind": "text",
                    "dataType": "text",
                    "nullable": True,
                    "constraints": [],
                },
            ],
        },
    ]
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={},
        writable_fields={},
    )

    await adapter.apply(_plan())
    await adapter.apply(_plan(values={"note": "fresh schema"}))

    assert [request["schemaRevision"] for request in client.requests] == [
        "schema-7",
        "schema-8",
    ]
