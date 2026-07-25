"""Product route adapter tests for relation import and Lookup export."""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.pocketbase.client import QueryPageResult
from backend.application.paste_service import ApplyPasteResult
from backend.application.relation_io_adapters import (
    PocketBaseLookupExportProvider,
    PocketBaseRelationImportProvider,
    RelationIoError,
)
from backend.contracts.data_io import ImportPlanRow
from backend.contracts.data_profile import CollectionProfile


def _page(rows: list[dict[str, Any]]) -> QueryPageResult:
    return QueryPageResult(
        rows=rows,
        offset=0,
        limit=2,
        filtered_rows=len(rows),
        total_rows=len(rows),
        snapshot={},
    )


class FakeProductClient:
    def __init__(self) -> None:
        self.query_calls: list[dict[str, Any]] = []
        self.lookup_calls: list[dict[str, Any]] = []
        self.schema_revision = "schema-1"

    async def describe_relations(self, table_id: str) -> dict[str, Any]:
        assert table_id == "orders"
        return {
            "schemaRevision": self.schema_revision,
            "relations": [
                {
                    "relationId": "orders_contract",
                    "sourceTableId": "orders",
                    "targetTableId": "contracts",
                    "physicalName": "contract",
                    "cardinality": "m2o",
                }
            ],
        }

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        assert table_id == "contracts"
        return {
            "tableId": "contracts",
            "fields": [
                {
                    "fieldId": "contract_number",
                    "physicalName": "number",
                    "constraints": [{"kind": "unique", "value": True}],
                },
                {
                    "fieldId": "contract_name",
                    "physicalName": "name",
                    "constraints": [],
                },
            ],
        }

    async def query_page(
        self,
        *,
        table_id: str,
        query: dict[str, Any],
    ) -> QueryPageResult:
        self.query_calls.append({"table_id": table_id, "query": query})
        return _page([{"id": "contract-1"}])

    async def describe_lookups(self, table_id: str) -> dict[str, Any]:
        assert table_id == "orders"
        return {
            "schemaRevision": self.schema_revision,
            "lookups": [
                {
                    "lookupId": "lookup_price",
                    "physicalName": "contract_price",
                }
            ],
        }

    async def query_lookups(
        self,
        *,
        table_id: str,
        schema_revision: str,
        query: dict[str, Any],
    ) -> QueryPageResult:
        self.lookup_calls.append(
            {
                "table_id": table_id,
                "schema_revision": schema_revision,
                "query": query,
            }
        )
        return _page(
            [
                {
                    "rowKey": "order-1",
                    "id": "order-1",
                    "contract_price": "12.50",
                }
            ]
        )


class FakeBulk:
    def __init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    async def apply(self, **kwargs: Any) -> ApplyPasteResult:
        self.calls.append(kwargs)
        rows = kwargs["rows"]
        return ApplyPasteResult(
            collection=kwargs["collection"],
            outcome="committed",
            created_row_keys=["order-new"] if rows[0].kind == "insert" else [],
            updated_row_keys=["order-1"] if rows[0].kind == "update" else [],
            request_id=kwargs["idempotency_key"],
        )


@pytest.mark.asyncio
async def test_relation_import_requires_unique_product_field_and_exact_query() -> None:
    client = FakeProductClient()
    provider = PocketBaseRelationImportProvider(client=client, bulk=FakeBulk())

    target = await provider.inspect_mapping(
        collection="orders",
        target_field="contract",
        relation_id="orders_contract",
        match_field="contract_number",
    )
    matches = await provider.find_exact(target, "C-001")

    assert target.target_collection == "contracts"
    assert target.match_field == "number"
    assert matches == ["contract-1"]
    assert client.query_calls == [
        {
            "table_id": "contracts",
            "query": {
                "filters": [
                    {
                        "field": "number",
                        "operator": "eq",
                        "value": "C-001",
                        "logic": "AND",
                    }
                ],
                "sorts": [],
                "offset": 0,
                "limit": 2,
            },
        }
    ]

    with pytest.raises(RelationIoError) as error:
        await provider.inspect_mapping(
            collection="orders",
            target_field="contract",
            relation_id="orders_contract",
            match_field="contract_name",
        )
    assert error.value.code == "relation_match_field_not_unique"


@pytest.mark.asyncio
async def test_relation_import_rejects_wrong_stable_relation_identity() -> None:
    provider = PocketBaseRelationImportProvider(client=FakeProductClient(), bulk=FakeBulk())

    with pytest.raises(RelationIoError) as error:
        await provider.inspect_mapping(
            collection="orders",
            target_field="contract",
            relation_id="different_relation",
            match_field="contract_number",
        )

    assert error.value.code == "relation_id_mismatch"


@pytest.mark.asyncio
async def test_relation_apply_submits_one_atomic_source_table_batch() -> None:
    bulk = FakeBulk()
    provider = PocketBaseRelationImportProvider(client=FakeProductClient(), bulk=bulk)
    result = await provider.apply_chunk(
        collection="orders",
        profile=CollectionProfile(
            collection="orders",
            schema_revision="schema-1",
            fields=["id", "contract"],
            create_fields=["id", "contract"],
            update_fields=["contract"],
            archive_field=None,
            date_updated_field=None,
        ),
        rows=[ImportPlanRow(source_row=2, values={"contract": "contract-1"})],
        mode="create_only",
        upsert_key=None,
        idempotency_key="import-1",
    )

    assert result.created_row_keys == ["order-new"]
    assert len(bulk.calls) == 1
    assert bulk.calls[0]["rows"][0].changes["contract"]["after"] == "contract-1"


@pytest.mark.asyncio
async def test_lookup_export_uses_stable_id_and_product_physical_field() -> None:
    client = FakeProductClient()
    provider = PocketBaseLookupExportProvider(client=client)

    page = await provider.query_page(
        collection="orders",
        fields=["id"],
        lookup_ids=["lookup_price"],
        lookup_revision="schema-1",
        query={"filters": []},
        offset=10,
        limit=25,
    )

    assert [(column.lookup_id, column.field_key) for column in page.columns] == [
        ("lookup_price", "contract_price")
    ]
    assert page.rows[0]["contract_price"] == "12.50"
    assert page.lookup_revision == "schema-1"
    assert client.lookup_calls[0] == {
        "table_id": "orders",
        "schema_revision": "schema-1",
        "query": {"filters": [], "offset": 10, "limit": 25},
    }


@pytest.mark.asyncio
async def test_lookup_export_rejects_catalog_revision_drift() -> None:
    client = FakeProductClient()
    client.schema_revision = "schema-2"
    provider = PocketBaseLookupExportProvider(client=client)

    with pytest.raises(RelationIoError) as error:
        await provider.query_page(
            collection="orders",
            fields=["id"],
            lookup_ids=["lookup_price"],
            lookup_revision="schema-1",
            query={},
            offset=0,
            limit=100,
        )

    assert error.value.code == "lookup_revision_mismatch"
    assert client.lookup_calls == []
