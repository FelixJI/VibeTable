from __future__ import annotations

from collections.abc import Mapping
from typing import Any

import httpx
import pytest
from pydantic import JsonValue, ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.plugin_mutation import PocketBasePluginMutationAdapter
from backend.adapters.pocketbase.transport import (
    PocketBaseConfig,
    PocketBaseTransportError,
    StdlibPocketBaseTransport,
)
from backend.contracts.plugin import MutationPlan, PluginCommitUnknownError, PluginExecutionError
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2


class FakeClient:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []
        self.definitions: list[dict[str, Any]] = []
        self.receipt_status = "applied"

    async def apply_mutation(self, request: Mapping[str, Any]) -> dict[str, Any]:
        self.requests.append(dict(request))
        return {
            "contractVersion": "2.0",
            "status": self.receipt_status,
            "affectedRows": [{"recordId": "1", "operation": "update"}],
        }

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
async def test_submission_boundary_is_not_entered_for_empty_or_rejected_plans() -> None:
    client = FakeClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )
    submitted = 0

    def on_submit() -> None:
        nonlocal submitted
        submitted += 1
        assert client.requests == []

    empty = MutationPlan.model_validate(
        {"collection": "orders", "operations": [], "preview": {"affectedCount": 0}}
    )
    assert (await adapter.apply(empty, on_submit=on_submit))["metrics"][0]["value"] == 0
    with pytest.raises(ValueError, match="field"):
        await adapter.apply(_plan(values={"secret": "forbidden"}), on_submit=on_submit)
    assert submitted == 0
    await adapter.apply(_plan(), on_submit=on_submit)
    assert submitted == 1


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
        snapshot_v2("orders", [field_v2("status")], revision="schema-7"),
        snapshot_v2("orders", [field_v2("status"), field_v2("note")], revision="schema-8"),
    ]
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={},
        writable_fields={},
    )

    await adapter.apply(_plan(values={"f_status00": "done"}))
    await adapter.apply(_plan(values={"f_note0000": "fresh schema"}))

    assert [request["schemaRevision"] for request in client.requests] == [
        "schema-7",
        "schema-8",
    ]


@pytest.mark.asyncio
async def test_explicit_digest_and_legacy_revision_reach_the_existing_go_guard() -> None:
    client = FakeClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )
    raw = _plan().model_dump(by_alias=True)
    raw["operations"][0].update(expectedDigest="sha256:" + "a" * 64, expectedDateUpdated="row_0001")
    await adapter.apply(MutationPlan.model_validate(raw))
    operation = client.requests[0]["operations"][0]
    assert operation["expectedDigest"] == "sha256:" + "a" * 64
    assert operation["expectedRevision"] == "row_0001"
    raw["operations"][0]["expectedDateUpdated"] = "2026-10-07"
    with pytest.raises(ValidationError):
        MutationPlan.model_validate(raw)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "outcome", ["pending", "transport", "incomplete", "wrong-record", "conflict"]
)
async def test_plugin_submission_preserves_conflict_and_unknown_ack(outcome: str) -> None:
    class BoundaryClient(FakeClient):
        async def apply_mutation(self, request: Mapping[str, Any]) -> dict[str, Any]:
            self.requests.append(dict(request))
            if outcome == "transport":
                raise PocketBaseTransportError("ack unavailable")
            if outcome == "conflict":
                raise PocketBaseProductError(
                    status=409,
                    payload={
                        "code": "mutation.digest_conflict",
                        "message": "row changed",
                        "details": {"expected": "old", "actual": "new"},
                    },
                )
            if outcome == "pending":
                return {"contractVersion": "2.0", "status": "pending"}
            if outcome == "wrong-record":
                return {
                    "contractVersion": "2.0",
                    "status": "applied",
                    "affectedRows": [{"recordId": "other", "operation": "update"}],
                }
            return {"status": "applied"}

    client = BoundaryClient()
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )
    with pytest.raises(
        PocketBaseProductError if outcome == "conflict" else PluginCommitUnknownError
    ) as raised:
        await adapter.apply(_plan())
    assert raised.value.code == (
        "mutation.digest_conflict" if outcome == "conflict" else "plugin_commit_unknown"
    )
    assert len(client.requests) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "receipt",
    [
        {},
        {"status": "unexpected"},
        {"status": []},
        [],
        None,
        {"contractVersion": "2.0", "status": "rejected"},
    ],
)
async def test_http_receipt_preserves_unknown_or_explicit_rejection_without_replay(
    receipt: JsonValue,
) -> None:
    requests: list[httpx.Request] = []

    def respond(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, json=receipt)

    config = PocketBaseConfig(base_url="http://127.0.0.1:8090", session_secret="a" * 64)
    client = PocketBaseClient(
        transport=StdlibPocketBaseTransport(config, http_transport=httpx.MockTransport(respond)),
        session_secret=config.session_secret,
    )
    adapter = PocketBasePluginMutationAdapter(
        client=client,
        schema_revisions={"orders": "schema-7"},
        writable_fields={"orders": {"status"}},
    )
    rejected = isinstance(receipt, dict) and receipt.get("status") == "rejected"
    with pytest.raises(PluginExecutionError if rejected else PluginCommitUnknownError) as raised:
        await adapter.apply(_plan())
    assert raised.value.code == (
        "plugin_mutation_rejected" if rejected else "plugin_commit_unknown"
    )
    assert len(requests) == 1
