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
