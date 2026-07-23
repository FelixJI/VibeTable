from __future__ import annotations

from typing import Any

import pytest

from backend.application.lookup_service import LookupService, LookupServiceError
from backend.contracts.lookup import (
    LookupCollectionParams,
    LookupCreateParams,
    LookupDefinition,
    LookupDeleteParams,
    LookupQueryParams,
    LookupUpdateParams,
    LookupValidateParams,
)
from backend.contracts.relation_admin import SchemaSnapshot
from backend.contracts.table import ColumnSchema


class _Auth:
    async def access_token(self) -> str:
        return "current-user"


class _Transport:
    def __init__(self) -> None:
        self.rows: dict[str, dict[str, Any]] = {}
        self.calls: list[tuple[str, str, dict[str, Any]]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.calls.append((method, path, kwargs))
        assert kwargs["access_token"] == "current-user"
        if method == "GET":
            raw_filter = kwargs["query"]["filter"]
            wanted = raw_filter["_and"][0]["collection"]["_eq"] if "_and" in raw_filter else None
            allowed_statuses = set(raw_filter.get("status", {}).get("_in", ["active"]))
            return {
                "data": [
                    row
                    for row in self.rows.values()
                    if (wanted is None or row["collection"] == wanted)
                    and row["status"] in allowed_statuses
                ]
            }
        if method == "POST":
            row = dict(kwargs["json_body"])
            self.rows[row["id"]] = row
            return {"data": row}
        if method == "PATCH":
            filters = kwargs["json_body"]["query"]["filter"]["_and"]
            record_id = filters[0]["id"]["_eq"]
            revision = filters[1]["revision"]["_eq"]
            row = self.rows.get(record_id)
            if row is None or row["revision"] != revision or row["status"] != "active":
                return {"data": []}
            row.update(kwargs["json_body"]["data"])
            return {"data": [row]}
        raise AssertionError((method, path))


def _definition(
    lookup_id: str = "contract_price",
    *,
    revision: int = 1,
    dependency: str | None = None,
) -> LookupDefinition:
    source = (
        {"kind": "lookup", "lookupId": dependency}
        if dependency
        else {"kind": "target_field", "fieldRef": "price"}
    )
    return LookupDefinition.model_validate(
        {
            "lookupId": lookup_id,
            "collection": "orders",
            "fieldKey": lookup_id,
            "displayName": "合同价格",
            "path": [{"relationId": "orders.contract"}],
            "source": source,
            "dependencies": [dependency] if dependency else [],
            "aggregation": "single",
            "outputType": "decimal",
            "outputScale": 2,
            "revision": revision,
        }
    )


@pytest.mark.asyncio
async def test_create_and_list_use_current_user_and_stable_revision() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    definition = _definition()

    created = await service.create(LookupCreateParams(definition=definition, request_id="r1"))
    listed = await service.list(LookupCollectionParams(collection="orders"))

    assert listed.definitions == [definition]
    assert listed.lookup_revision == created.lookup_revision
    create_call = next(call for call in transport.calls if call[0] == "POST")
    assert create_call[2]["headers"] == {"X-Request-ID": "r1"}


@pytest.mark.asyncio
async def test_create_is_idempotent_but_rejects_identity_reuse() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    original = _definition()
    await service.create(LookupCreateParams(definition=original, request_id="r1"))
    same = await service.create(LookupCreateParams(definition=original, request_id="r1"))
    assert same.definition == original

    changed = original.model_copy(update={"display_name": "changed"})
    with pytest.raises(LookupServiceError, match="already exists"):
        await service.create(LookupCreateParams(definition=changed, request_id="r2"))


@pytest.mark.asyncio
async def test_validate_rejects_duplicate_field_key_in_same_collection() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    first = _definition("first").model_copy(update={"field_key": "shared_price"})
    second = _definition("second").model_copy(update={"field_key": "shared_price"})
    await service.create(LookupCreateParams(definition=first, request_id="r1"))

    with pytest.raises(LookupServiceError) as error:
        await service.validate(LookupValidateParams(definition=second, existing=[first]))

    assert error.value.code == "lookup_field_conflict"
    assert "shared_price" in str(error.value)


@pytest.mark.asyncio
async def test_validate_rejects_visible_physical_field_key() -> None:
    transport = _Transport()

    async def schema_provider(collection: str) -> SchemaSnapshot:
        return SchemaSnapshot(
            collection=collection,
            primary_key="id",
            columns=[
                ColumnSchema(
                    name="contract_price",
                    title="Stored contract price",
                    field_id=f"{collection}.contract_price",
                    data_type="decimal",
                )
            ],
            schema_revision="schema-1",
            permission_revision="permission-1",
            capability_hash="capability-1",
            lookup_revision="lookup-1",
        )

    service = LookupService(
        transport=transport,
        auth=_Auth(),
        project="p",
        schema_provider=schema_provider,
    )

    with pytest.raises(LookupServiceError) as error:
        await service.validate(LookupValidateParams(definition=_definition(), existing=[]))

    assert error.value.code == "lookup_field_conflict"
    assert "physical" in str(error.value)


@pytest.mark.asyncio
async def test_update_uses_revision_cas_and_increments_revision() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    await service.create(LookupCreateParams(definition=_definition(), request_id="r1"))

    result = await service.update(
        LookupUpdateParams(
            definition=_definition().model_copy(update={"display_name": "新价格"}),
            expected_revision=1,
            request_id="r2",
        )
    )

    assert result.definition is not None
    assert result.definition.revision == 2
    with pytest.raises(LookupServiceError, match="changed by another user"):
        await service.update(
            LookupUpdateParams(
                definition=_definition(revision=2), expected_revision=1, request_id="r3"
            )
        )


@pytest.mark.asyncio
async def test_delete_rejects_live_dependents() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    base = _definition("base")
    dependent = _definition("dependent", dependency="base")
    await service.create(LookupCreateParams(definition=base, request_id="r1"))
    await service.create(LookupCreateParams(definition=dependent, request_id="r2"))

    with pytest.raises(LookupServiceError, match="referenced by"):
        await service.delete(
            LookupDeleteParams(
                collection="orders", lookup_id="base", expected_revision=1, request_id="r3"
            )
        )


@pytest.mark.asyncio
async def test_cross_collection_dependencies_list_and_delete_safely() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    target = LookupDefinition.model_validate(
        {
            "lookupId": "contracts.tax_rate",
            "collection": "contracts",
            "fieldKey": "tax_rate",
            "displayName": "Tax rate",
            "path": [{"relationId": "contracts.tax_code"}],
            "source": {"kind": "target_field", "fieldRef": "rate"},
            "outputType": "decimal",
            "outputScale": 4,
        }
    )
    dependent = LookupDefinition.model_validate(
        {
            "lookupId": "orders.contract_tax_rate",
            "collection": "orders",
            # fieldKey uniqueness is scoped to a collection; this deliberately
            # matches the target collection's key while keeping a valid
            # cross-collection Lookup dependency.
            "fieldKey": "tax_rate",
            "displayName": "Contract tax rate",
            "path": [{"relationId": "orders.contract"}],
            "source": {"kind": "lookup", "lookupId": "contracts.tax_rate"},
            "dependencies": ["contracts.tax_rate"],
            "outputType": "decimal",
            "outputScale": 4,
        }
    )
    await service.create(LookupCreateParams(definition=target, request_id="r1"))
    await service.create(LookupCreateParams(definition=dependent, request_id="r2"))

    listed = await service.list(LookupCollectionParams(collection="orders"))
    assert listed.definitions == [dependent]
    with pytest.raises(LookupServiceError, match=r"orders\.contract_tax_rate"):
        await service.delete(
            LookupDeleteParams(
                collection="contracts",
                lookup_id="contracts.tax_rate",
                expected_revision=1,
                request_id="r3",
            )
        )


@pytest.mark.asyncio
async def test_list_rejects_hidden_row_identity_mismatch() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    await service.create(LookupCreateParams(definition=_definition("price"), request_id="r1"))
    row = next(iter(transport.rows.values()))
    row["lookup_id"] = "tampered"

    with pytest.raises(LookupServiceError, match="identity is inconsistent"):
        await service.list(LookupCollectionParams(collection="orders"))


@pytest.mark.asyncio
async def test_cascade_delete_is_dependent_first_and_retry_safe() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    base = _definition("base")
    dependent = _definition("dependent", dependency="base")
    await service.create(LookupCreateParams(definition=base, request_id="r1"))
    await service.create(LookupCreateParams(definition=dependent, request_id="r2"))

    await service.cascade_delete(["base", "dependent"], "cascade")
    await service.cascade_delete(["base", "dependent"], "cascade")

    patch_ids = [
        call[2]["json_body"]["query"]["filter"]["_and"][0]["id"]["_eq"]
        for call in transport.calls
        if call[0] == "PATCH"
    ]
    assert patch_ids == [service._record_id(dependent), service._record_id(base)]
    assert {row["status"] for row in transport.rows.values()} == {"deleted"}


@pytest.mark.asyncio
async def test_cycle_is_rejected_before_persistence() -> None:
    transport = _Transport()
    service = LookupService(transport=transport, auth=_Auth(), project="p")
    a = _definition("a")
    b = _definition("b", dependency="a")
    await service.create(LookupCreateParams(definition=a, request_id="r1"))
    await service.create(LookupCreateParams(definition=b, request_id="r2"))

    with pytest.raises(LookupServiceError, match="cycle"):
        await service.update(
            LookupUpdateParams(
                definition=_definition("a", dependency="b"),
                expected_revision=1,
                request_id="r3",
            )
        )


@pytest.mark.asyncio
async def test_query_compiles_private_plan_and_translates_authoritative_result(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    clock = [100.0]
    monkeypatch.setattr("backend.application.lookup_service.time.monotonic", lambda: clock[0])
    definition = _definition()

    class QueryTransport:
        def __init__(self) -> None:
            self.plan: dict[str, Any] | None = None
            self.query_count = 0

        async def request(self, method: str, path: str, **kwargs: Any) -> Any:
            if method == "GET":
                return {
                    "data": [
                        {
                            "lookup_id": definition.lookup_id,
                            "revision": 1,
                            "definition": definition.model_dump(mode="json", by_alias=True),
                        }
                    ]
                }
            self.plan = kwargs["json_body"]
            self.query_count += 1
            return {
                "data": {
                    "contract": "vibetable-lookup-query.v1",
                    "generation": self.plan["generation"],
                    "revisions": {
                        "schema": "schema-1",
                        "permission": "permission-1",
                        "lookup": revision,
                    },
                    "rootTotal": 3,
                    "total": 2,
                    "rows": [
                        {
                            "primaryKey": "order-1",
                            "cells": {"orders.contract_price": "1250.50"},
                        }
                    ],
                    "groups": [],
                    "page": {"offset": 0, "limit": 100},
                    "stableTieBreaker": "id",
                }
            }

    class QueryClient:
        async def relation_lookup_capabilities(self) -> dict[str, Any]:
            return {"lookup_query_v1": True, "relation_edit_v1": True}

        async def schema_fields(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True},
                },
                {
                    "collection": "orders",
                    "field": "contract",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": False},
                },
                {
                    "collection": "contracts",
                    "field": "id",
                    "type": "uuid",
                    "meta": {},
                    "schema": {"is_primary_key": True},
                },
                {
                    "collection": "contracts",
                    "field": "price",
                    "type": "decimal",
                    "meta": {},
                    "schema": {"is_primary_key": False},
                },
            ]

        async def schema_relations(self) -> list[dict[str, Any]]:
            return [
                {
                    "collection": "orders",
                    "field": "contract",
                    "related_collection": "contracts",
                    "meta": {
                        "many_collection": "orders",
                        "many_field": "contract",
                        "one_collection": "contracts",
                    },
                    "schema": {"on_delete": "SET NULL"},
                }
            ]

    transport = QueryTransport()
    provisional = LookupService(transport=transport, auth=_Auth(), project="p")
    revision = (await provisional.list(LookupCollectionParams(collection="orders"))).lookup_revision

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return SchemaSnapshot(
            collection="orders",
            primary_key="id",
            columns=[ColumnSchema(name="id", title="ID", field_id="orders.id", data_type="text")],
            schema_revision="schema-1",
            permission_revision="permission-1",
            capability_hash="capability-1",
            lookup_revision=revision,
        )

    service = LookupService(
        transport=transport,
        auth=_Auth(),
        project="p",
        client=QueryClient(),
        schema_provider=schema_provider,
        query_cache_max_entries=1,
    )
    result = await service.query(
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.contract_price"],
                "requestGeneration": 4,
                "schemaRevision": "schema-1",
                "permissionRevision": "permission-1",
                "lookupRevision": revision,
            }
        )
    )

    assert transport.plan is not None
    assert transport.plan["contract"] == "vibetable-lookup-query.v1"
    assert transport.plan["lookups"][0]["aggregate"] == "scalar"
    assert result.rows == [{"rowKey": "order-1", "orders.contract_price": "1250.50"}]
    assert (result.filtered_rows, result.total_rows) == (2, 3)
    assert transport.query_count == 1

    cached = await service.query(
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.contract_price"],
                "requestGeneration": 5,
                "schemaRevision": "schema-1",
                "permissionRevision": "permission-1",
                "lookupRevision": revision,
            }
        )
    )
    assert cached.request_generation == 5
    assert transport.query_count == 1

    service.invalidate_collection("contracts")
    refreshed = await service.query(
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.contract_price"],
                "requestGeneration": 6,
                "schemaRevision": "schema-1",
                "permissionRevision": "permission-1",
                "lookupRevision": revision,
            }
        )
    )
    assert refreshed.request_generation == 6
    assert transport.query_count == 2

    second_query = LookupQueryParams.model_validate(
        {
            "collection": "orders",
            "fieldRefs": ["orders.contract_price"],
            "query": {"limit": 50},
            "requestGeneration": 7,
            "schemaRevision": "schema-1",
            "permissionRevision": "permission-1",
            "lookupRevision": revision,
        }
    )
    await service.query(second_query)
    assert transport.query_count == 3
    assert len(service._query_cache) == 1

    # The second query evicted the first (LRU capacity=1), so the first misses.
    await service.query(
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.contract_price"],
                "requestGeneration": 8,
                "schemaRevision": "schema-1",
                "permissionRevision": "permission-1",
                "lookupRevision": revision,
            }
        )
    )
    assert transport.query_count == 4

    # Expired entries are also discarded, while the cache stays bounded.
    clock[0] += 3.0
    await service.query(
        LookupQueryParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["orders.contract_price"],
                "requestGeneration": 9,
                "schemaRevision": "schema-1",
                "permissionRevision": "permission-1",
                "lookupRevision": revision,
            }
        )
    )
    assert transport.query_count == 5
    assert len(service._query_cache) == 1
