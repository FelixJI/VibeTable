from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.application.paste_service import PastePlanRow
from backend.contracts.data_profile import CollectionProfile


class _User:
    id = "user-1"
    first_name = "Ada"
    last_name = "Lovelace"


class _Auth:
    async def current_user(self) -> _User:
        return _User()


class _Client:
    def __init__(self, response: Any) -> None:
        self.response = response
        self.requests: list[dict[str, Any]] = []

    async def apply_mutation(self, request: dict[str, Any]) -> dict[str, Any]:
        self.requests.append(request)
        if isinstance(self.response, Exception):
            raise self.response
        return self.response

    async def preview_import(self, request: dict[str, Any]) -> dict[str, Any]:
        self.requests.append(request)
        return {
            "contract": "vibetable.import-preview.v1",
            "rows": [{"values": {}, "diagnostics": []}],
        }

    async def preview_mutation(self, request: dict[str, Any]) -> dict[str, Any]:
        self.requests.append(request)
        return {"operations": []}


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="orders",
        primary_key="id",
        fields=["id", "title", "payload"],
        create_fields=["title", "payload"],
        update_fields=["title", "payload"],
        archive_field=None,
        date_updated_field=None,
    )


@pytest.mark.asyncio
async def test_bulk_mutation_compiles_one_frozen_kernel_request() -> None:
    client = _Client(
        {
            "contractVersion": "1.0",
            "status": "applied",
            "changeSetId": "change-1",
            "affectedRows": [
                {
                    "recordId": "row-1",
                    "operation": "update",
                    "revision": "row_2",
                    "digest": "sha256:" + ("a" * 64),
                },
                {
                    "recordId": "row-2",
                    "operation": "insert",
                    "revision": "row_1",
                    "digest": "sha256:" + ("b" * 64),
                },
            ],
            "computedFields": {},
            "newRevision": "data_3",
            "emittedEvents": [],
            "warnings": [],
        }
    )
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())
    rows = [
        PastePlanRow(
            kind="update",
            target_row_key="row-1",
            changes={"title": {"before": "old", "after": "new"}},
        ),
        PastePlanRow(
            kind="insert",
            changes={
                "payload": {
                    "before": None,
                    "after": {"nested": [1, True, None]},
                }
            },
        ),
    ]

    result = await bulk.apply(
        collection="orders",
        profile=_profile(),
        rows=rows,
        row_revisions={},
        idempotency_key="paste-1",
        schema_revision="schema_7",
    )

    assert result.outcome == "committed"
    assert result.updated_row_keys == ["row-1"]
    assert result.created_row_keys == ["row-2"]
    assert client.requests == [
        {
            "contractVersion": "1.0",
            "requestId": "paste-1",
            "idempotencyKey": "paste-1",
            "tableId": "orders",
            "schemaRevision": "schema_7",
            "operations": [
                {"kind": "update", "recordId": "row-1", "values": {"title": "new"}},
                {
                    "kind": "insert",
                    "recordId": None,
                    "values": {"payload": {"nested": [1, True, None]}},
                },
            ],
            "actor": {
                "type": "user",
                "id": "user-1",
                "displayName": "Ada Lovelace",
            },
            "expectedRevision": None,
            "expectedDigest": None,
        }
    ]


@pytest.mark.asyncio
async def test_preview_import_forwards_row_modes_to_go() -> None:
    client = _Client({})
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())

    await bulk.preview_import(
        collection="orders",
        schema_revision="schema_7",
        rows=[{"title": "updated"}],
        row_modes=["update"],
    )

    assert client.requests == [
        {
            "contract": "vibetable.import-preview.v1",
            "tableId": "orders",
            "schemaRevision": "schema_7",
            "rows": [{"values": {"title": "updated"}, "mode": "update"}],
        }
    ]


@pytest.mark.asyncio
async def test_empty_insert_reaches_kernel_for_unsupplied_defaults() -> None:
    client = _Client(
        {
            "contractVersion": "1.0",
            "status": "applied",
            "changeSetId": "change-default",
            "affectedRows": [
                {
                    "recordId": "row-default",
                    "operation": "insert",
                    "revision": "row_1",
                    "digest": "sha256:" + ("c" * 64),
                }
            ],
            "computedFields": {},
            "newRevision": "data_1",
            "emittedEvents": [],
            "warnings": [],
        }
    )
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())

    result = await bulk.apply(
        collection="orders",
        profile=_profile(),
        rows=[PastePlanRow(kind="insert", changes={})],
        raw_rows=[{}],
        row_revisions={},
        idempotency_key="insert-defaults",
        schema_revision="schema_7",
    )

    assert result.created_row_keys == ["row-default"]
    assert client.requests[0]["operations"] == [
        {
            "kind": "insert",
            "recordId": None,
            "values": {},
            "rawValues": {},
        }
    ]


@pytest.mark.asyncio
async def test_paste_compiles_raw_values_for_authoritative_kernel_normalization() -> None:
    client = _Client(
        {
            "contractVersion": "1.0",
            "status": "applied",
            "changeSetId": "change-raw",
            "affectedRows": [
                {
                    "recordId": "row-1",
                    "operation": "update",
                    "revision": "row_2",
                    "digest": "sha256:" + ("a" * 64),
                }
            ],
            "computedFields": {},
            "newRevision": "data_3",
            "emittedEvents": [],
            "warnings": [],
        }
    )
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())

    await bulk.apply(
        collection="orders",
        profile=_profile(),
        rows=[
            PastePlanRow(
                kind="update",
                target_row_key="row-1",
                changes={
                    "payload": {
                        "before": None,
                        "after": {"ok": True},
                    }
                },
            )
        ],
        row_revisions={},
        idempotency_key="paste-raw",
        schema_revision="schema_7",
        raw_rows=[{"payload": '{"ok":true}'}],
    )

    assert client.requests[0]["operations"] == [
        {
            "kind": "update",
            "recordId": "row-1",
            "values": {},
            "rawValues": {"payload": '{"ok":true}'},
        }
    ]


@pytest.mark.asyncio
async def test_bulk_mutation_maps_kernel_conflict_without_fallback_write() -> None:
    client = _Client(
        PocketBaseProductError(
            status=409,
            payload={
                "code": "mutation.revision_conflict",
                "message": "changed",
                "details": {"actual": "row_3"},
                "retryable": False,
            },
        )
    )
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())

    result = await bulk.apply(
        collection="orders",
        profile=_profile(),
        rows=[
            PastePlanRow(
                kind="update",
                target_row_key="row-1",
                changes={"title": {"before": "old", "after": "new"}},
            )
        ],
        row_revisions={"row-1": "row_2"},
        idempotency_key="paste-conflict",
        schema_revision="schema_7",
    )

    assert result.outcome == "conflict"
    assert result.conflicts[0].row_key == "row-1"
    assert client.requests[0]["expectedRevision"] is None
    assert client.requests[0]["operations"][0]["expectedRevision"] == "row_2"


@pytest.mark.asyncio
async def test_bulk_conflict_ignores_normalized_noop_when_mapping_row_guard() -> None:
    client = _Client(
        PocketBaseProductError(
            status=409,
            payload={
                "code": "mutation.revision_conflict",
                "message": "changed",
                "details": {"actual": "row_3"},
                "retryable": False,
            },
        )
    )
    bulk = PocketBaseBulkMutationClient(client=client, auth=_Auth())

    result = await bulk.apply(
        collection="orders",
        profile=_profile(),
        rows=[
            PastePlanRow(
                kind="update",
                target_row_key="row-noop",
                changes={},
            ),
            PastePlanRow(
                kind="update",
                target_row_key="row-real",
                changes={"title": {"before": "old", "after": "new"}},
            ),
        ],
        row_revisions={
            "row-noop": "row_1",
            "row-real": "row_2",
        },
        idempotency_key="paste-mixed-conflict",
        schema_revision="schema_7",
    )

    assert result.outcome == "conflict"
    assert result.conflicts[0].row_key == "row-real"
    assert result.conflicts[0].expected_date_updated == "row_2"
    assert client.requests[0]["expectedRevision"] is None
    assert [operation["recordId"] for operation in client.requests[0]["operations"]] == ["row-real"]
    assert client.requests[0]["operations"][0]["expectedRevision"] == "row_2"
