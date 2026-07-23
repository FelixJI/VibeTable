from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.profile import CollectionProfile
from backend.application.relation_io_adapters import (
    DirectusRelationImportProvider,
    LookupExportProvider,
    RelationIoError,
)
from backend.contracts.data_io import ImportPlanRow, ImportRelationResolution
from backend.contracts.lookup import (
    LookupDefinition,
    LookupListResult,
    LookupQueryResult,
)
from backend.contracts.relation_admin import NormalizedRelationDescriptor, SchemaSnapshot


class _Auth:
    async def access_token(self) -> str:
        return "user-token"


class _Client:
    async def relation_lookup_capabilities(self) -> dict[str, Any]:
        return {"relation_import_v1": True}

    async def schema_fields(self) -> list[dict[str, Any]]:
        return [
            {
                "collection": "orders",
                "field": "id",
                "type": "uuid",
                "schema": {"is_primary_key": True},
            },
            {
                "collection": "orders",
                "field": "contract",
                "type": "uuid",
                "schema": {},
            },
            {
                "collection": "contracts",
                "field": "id",
                "type": "uuid",
                "schema": {"is_primary_key": True},
            },
            {
                "collection": "contracts",
                "field": "number",
                "type": "string",
                "schema": {"is_unique": True},
            },
            {
                "collection": "contracts",
                "field": "name",
                "type": "string",
                "schema": {},
            },
        ]


class _Transport:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, dict[str, Any]]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.calls.append((method, path, kwargs))
        if method == "GET":
            return {"data": [{"id": "contract-1"}]}
        return {
            "data": {
                "outcome": "committed",
                "createdSourceRowKeys": ["order-1"],
                "updatedSourceRowKeys": [],
                "requestId": "import-1",
            }
        }


def _snapshot() -> SchemaSnapshot:
    return SchemaSnapshot(
        collection="orders",
        primary_key="id",
        columns=[],
        normalized_relations=[],
        schema_revision="schema-1",
        permission_revision="permission-1",
        capability_hash="capability-1",
        lookup_revision="lookup-1",
    )


async def _resolve(_relation_id: str):
    return _snapshot(), NormalizedRelationDescriptor(
        relation_id="orders.contract",
        field_ref="orders.contract",
        source_collection="orders",
        kind="m2o",
        related_collection="contracts",
        many_field="contract",
    )


@pytest.mark.asyncio
async def test_relation_import_requires_explicit_unique_field_and_exact_match() -> None:
    transport = _Transport()
    provider = DirectusRelationImportProvider(
        client=_Client(),
        transport=transport,
        auth=_Auth(),
        resolve_relation=_resolve,
    )
    target = await provider.inspect_mapping(
        collection="orders",
        target_field="contract",
        relation_id="orders.contract",
        match_field="number",
    )
    assert await provider.find_exact(target, "C-001") == ["contract-1"]
    assert transport.calls[0][2]["query"]["filter"] == {"number": {"_eq": "C-001"}}

    with pytest.raises(RelationIoError) as exc:
        await provider.inspect_mapping(
            collection="orders",
            target_field="contract",
            relation_id="orders.contract",
            match_field="name",
        )
    assert exc.value.code == "relation_match_field_not_unique"


@pytest.mark.asyncio
async def test_relation_import_apply_compiles_extension_schema_proof() -> None:
    transport = _Transport()
    provider = DirectusRelationImportProvider(
        client=_Client(),
        transport=transport,
        auth=_Auth(),
        resolve_relation=_resolve,
    )
    await provider.inspect_mapping(
        collection="orders",
        target_field="contract",
        relation_id="orders.contract",
        match_field="number",
    )
    result = await provider.apply_chunk(
        collection="orders",
        profile=CollectionProfile(
            collection="orders",
            fields=["id", "contract"],
            create_fields=["id", "contract"],
            update_fields=["contract"],
            archive_field=None,
            date_updated_field=None,
        ),
        rows=[
            ImportPlanRow(
                source_row=2,
                values={"contract": "contract-1"},
                relation_resolutions=[
                    ImportRelationResolution(
                        target_field="contract",
                        relation_id="orders.contract",
                        match_field="number",
                        source_value="C-001",
                        state="matched",
                        matched_primary_key="contract-1",
                    )
                ],
            )
        ],
        mode="create_only",
        upsert_key=None,
        idempotency_key="import-1",
    )
    assert result.created_row_keys == ["order-1"]
    body = transport.calls[-1][2]["json_body"]
    assert body["mode"] == "create"
    assert body["schemaProof"]["uniqueFields"]["contracts"] == ["number"]
    assert body["rows"][0]["values"] == {}
    assert body["rows"][0]["relations"][0]["targetCollection"] == "contracts"


@pytest.mark.asyncio
async def test_relation_import_rejects_schema_revision_drift_after_preview() -> None:
    revision = "schema-1"

    async def resolve(_relation_id: str):
        return _snapshot().model_copy(update={"schema_revision": revision}), (
            NormalizedRelationDescriptor(
                relation_id="orders.contract",
                field_ref="orders.contract",
                source_collection="orders",
                kind="m2o",
                related_collection="contracts",
                many_field="contract",
            )
        )

    transport = _Transport()
    provider = DirectusRelationImportProvider(
        client=_Client(),
        transport=transport,
        auth=_Auth(),
        resolve_relation=resolve,
    )
    await provider.inspect_mapping(
        collection="orders",
        target_field="contract",
        relation_id="orders.contract",
        match_field="number",
    )
    revision = "schema-2"
    row = ImportPlanRow(
        source_row=2,
        values={"contract": "contract-1"},
        relation_resolutions=[
            ImportRelationResolution(
                target_field="contract",
                relation_id="orders.contract",
                match_field="number",
                source_value="C-001",
                state="matched",
                matched_primary_key="contract-1",
            )
        ],
    )

    with pytest.raises(RelationIoError) as exc:
        await provider.apply_chunk(
            collection="orders",
            profile=CollectionProfile(
                collection="orders",
                fields=["id", "contract"],
                create_fields=["id", "contract"],
                update_fields=["contract"],
                archive_field=None,
                date_updated_field=None,
            ),
            rows=[row],
            mode="create_only",
            upsert_key=None,
            idempotency_key="drifted-import",
        )
    assert exc.value.code == "relation_import_proof_expired"
    assert not any(method == "POST" for method, _path, _kwargs in transport.calls)


@pytest.mark.asyncio
async def test_lookup_export_remaps_lookup_identity_to_field_key() -> None:
    definition = LookupDefinition.model_validate(
        {
            "lookupId": "lookup-price",
            "collection": "orders",
            "fieldKey": "contract_price",
            "displayName": "合同价格",
            "path": [{"relationId": "orders.contract"}],
            "source": {"kind": "target_field", "fieldRef": "price"},
            "aggregation": "single",
            "outputType": "decimal",
            "outputScale": 2,
        }
    )

    class _Lookup:
        async def query(self, params: Any) -> LookupQueryResult:
            return LookupQueryResult(
                collection="orders",
                request_generation=params.request_generation,
                schema_revision="schema-1",
                permission_revision="permission-1",
                lookup_revision="lookup-1",
                columns=[
                    {
                        "fieldRef": "lookup-price",
                        "title": "合同价格",
                        "outputType": "decimal",
                    }
                ],
                rows=[{"rowKey": "order-1", "id": "order-1", "lookup-price": "12.50"}],
                offset=0,
                limit=100,
                filtered_rows=1,
                total_rows=1,
            )

        async def list(self, _params: Any) -> LookupListResult:
            return LookupListResult(
                collection="orders",
                definitions=[definition],
                lookup_revision="lookup-1",
            )

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return _snapshot()

    provider = LookupExportProvider(
        lookup_service=_Lookup(),  # type: ignore[arg-type]
        schema_provider=schema_provider,
    )
    page = await provider.query_page(
        collection="orders",
        fields=["id"],
        lookup_ids=["lookup-price"],
        lookup_revision="lookup-1",
        query={},
        offset=0,
        limit=100,
    )
    assert page.columns[0].field_key == "contract_price"
    assert page.rows == [{"rowKey": "order-1", "id": "order-1", "contract_price": "12.50"}]
