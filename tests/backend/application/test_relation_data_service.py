from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.profile import CollectionProfile, JunctionProfile
from backend.application.relation_data_service import RelationDataError, RelationDataService
from backend.contracts.relation_admin import (
    NormalizedRelationDescriptor,
    RelationAdd,
    RelationDelta,
    RelationJunctionPatch,
    RelationSearchParams,
    RelationSingleUpdateParams,
    RelationTargetRef,
    SchemaSnapshot,
)


class _Auth:
    async def access_token(self) -> str:
        return "user-token"


class _Client:
    def __init__(self) -> None:
        self.update: tuple[Any, ...] | None = None

    async def update_item(
        self, profile: Any, item_id: str, values: dict[str, Any], **kwargs: Any
    ) -> dict[str, Any]:
        self.update = (profile.collection, item_id, values, kwargs)
        return {profile.primary_key: item_id, **values}

    async def schema_fields(self) -> list[dict[str, Any]]:
        return []


class _Transport:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, dict[str, Any]]] = []
        self.rows: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.calls.append((method, path, kwargs))
        assert kwargs["access_token"] == "user-token"
        if method == "GET":
            return {"data": self.rows, "meta": {"filter_count": len(self.rows)}}
        if method == "POST":
            return {"data": {"outcome": "committed", "requestId": "delta-1"}}
        raise AssertionError((method, path))


def _snapshot(relation: NormalizedRelationDescriptor) -> SchemaSnapshot:
    return SchemaSnapshot(
        collection="orders",
        primary_key="id",
        columns=[],
        normalized_relations=[relation],
        schema_revision="schema-1",
        permission_revision="permission-1",
        capability_hash="capability-1",
        lookup_revision="lookup-1",
    )


def _service(
    relation: NormalizedRelationDescriptor,
) -> tuple[RelationDataService, _Client, _Transport]:
    client = _Client()
    transport = _Transport()
    snapshot = _snapshot(relation)

    async def resolve(relation_id: str):
        assert relation_id == relation.relation_id
        return snapshot, relation

    profiles = {
        "orders": CollectionProfile(
            collection="orders",
            fields=["id", "contract", "date_updated"],
            create_fields=["id", "contract"],
            update_fields=["contract"],
            archive_field=None,
        ),
        "contracts": CollectionProfile(
            collection="contracts",
            fields=["id", "number", "price", "date_updated"],
            create_fields=["id", "number", "price"],
            update_fields=["number", "price"],
            archive_field=None,
        ),
        "tags": CollectionProfile(
            collection="tags",
            fields=["id", "date_updated"],
            create_fields=["id"],
            update_fields=[],
            archive_field=None,
        ),
    }
    service = RelationDataService(
        client=client,  # type: ignore[arg-type]
        auth=_Auth(),
        transport=transport,
        profiles=profiles,
        resolve_relation=resolve,
    )
    return service, client, transport


@pytest.mark.asyncio
async def test_target_search_uses_only_explicit_template_fields() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="rel-1",
        field_ref="orders.contract",
        source_collection="orders",
        kind="m2o",
        related_collection="contracts",
        many_field="contract",
        display_template="{{number}}",
    )
    service, _client, transport = _service(relation)
    transport.rows = [{"id": "c1", "number": "HT-001"}]

    result = await service.search_targets(RelationSearchParams(relation_id="rel-1", query="HT"))

    query = transport.calls[0][2]["query"]
    assert query["fields"] == ["id", "number"]
    assert query["filter"] == {"_or": [{"number": {"_icontains": "HT"}}]}
    assert result.items[0].label == "HT-001"


@pytest.mark.asyncio
async def test_target_search_without_template_does_not_guess_common_fields() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="rel-1",
        field_ref="orders.contract",
        source_collection="orders",
        kind="m2o",
        related_collection="contracts",
        many_field="contract",
    )
    service, _client, transport = _service(relation)
    transport.rows = [{"id": "c1"}]

    await service.search_targets(RelationSearchParams(relation_id="rel-1", query="c1"))

    query = transport.calls[0][2]["query"]
    assert query["fields"] == ["id"]
    assert query["filter"] == {"id": {"_eq": "c1"}}


@pytest.mark.asyncio
async def test_single_relation_commits_immediately_with_cas() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="rel-1",
        field_ref="orders.contract",
        source_collection="orders",
        kind="m2o",
        related_collection="contracts",
        many_field="contract",
    )
    service, client, _transport = _service(relation)
    target = RelationTargetRef(collection="contracts", item_id="c1", label="c1")

    result = await service.update_single(
        RelationSingleUpdateParams(
            relation_id="rel-1",
            source_item_id="o1",
            target=target,
            expected_schema_revision="schema-1",
            expected_date_updated="2026-01-01T00:00:00Z",
            idempotency_key="update-1",
        )
    )

    assert result.current == target
    assert client.update == (
        "orders",
        "o1",
        {"contract": "c1"},
        {
            "expected_date_updated": "2026-01-01T00:00:00Z",
            "request_id": "update-1",
        },
    )


@pytest.mark.asyncio
async def test_multi_relation_preview_then_atomic_delta_apply() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="rel-tags",
        field_ref="orders.tags",
        source_collection="orders",
        kind="m2m",
        related_collection="tags",
        one_field="tags",
        junction=JunctionProfile(
            collection="order_tags",
            source_field="order",
            target_field="tag",
            context_fields=["quantity"],
        ),
    )
    service, _client, transport = _service(relation)
    transport.rows = []
    delta = RelationDelta(
        relation_id="rel-tags",
        source_item_id="o1",
        expected_schema_revision="schema-1",
        adds=[
            RelationAdd(
                target=RelationTargetRef(
                    collection="tags",
                    item_id="t1",
                    label="t1",
                    junction_values={"quantity": 2},
                )
            )
        ],
        idempotency_key="delta-1",
    )

    preview = await service.preview_delta(delta)
    result = await service.apply_delta(delta)

    assert preview.can_apply is True
    assert result.outcome == "committed"
    post = next(call for call in transport.calls if call[0] == "POST")
    assert post[1] == "/vibetable-bulk-mutation/relation-delta"
    assert post[2]["json_body"]["relation"]["junction"]["contextFields"] == ["quantity"]
    assert post[2]["json_body"]["schemaProof"] == (
        "e03aa90bbebdaefe8fab31f72330da9158a02de7286d8194c63da48e3230ec0c"
    )


@pytest.mark.asyncio
async def test_junction_preview_emits_revision_and_update_requires_it() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="rel-tags",
        field_ref="orders.tags",
        source_collection="orders",
        kind="m2m",
        related_collection="tags",
        junction=JunctionProfile(
            collection="order_tags",
            source_field="order",
            target_field="tag",
            context_fields=["quantity"],
        ),
    )
    service, _client, transport = _service(relation)
    transport.rows = [{"id": "j1", "order": "o1", "tag": "t1", "quantity": 2}]
    unchanged = RelationDelta(
        relation_id="rel-tags",
        source_item_id="o1",
        expected_schema_revision="schema-1",
        idempotency_key="preview-1",
    )
    preview = await service.preview_delta(unchanged)
    revision = preview.current[0].junction_revision
    assert revision is not None
    assert revision == "0218b8f7bac85ccdbc62269def9a7a31f237467244e7233c3e5d2caf2dea0b83"

    missing_revision = unchanged.model_copy(
        update={"updates": [RelationJunctionPatch(junction_id="j1", values={"quantity": 3})]}
    )
    blocked = await service.preview_delta(missing_revision)
    assert blocked.can_apply is False
    assert [item.code for item in blocked.diagnostics] == ["junction_revision_required"]

    with_revision = missing_revision.model_copy(
        update={
            "updates": [
                RelationJunctionPatch(
                    junction_id="j1",
                    values={"quantity": 3},
                    expected_revision=revision,
                )
            ]
        }
    )
    await service.apply_delta(with_revision)
    post = next(call for call in transport.calls if call[0] == "POST")
    assert post[2]["json_body"]["relation"]["updates"][0]["expectedRevision"] == revision


@pytest.mark.asyncio
async def test_invalid_relation_state_fails_closed() -> None:
    relation = NormalizedRelationDescriptor(
        relation_id="broken",
        field_ref="orders.tags",
        source_collection="orders",
        kind="m2m",
        state="invalid",
    )
    service, _client, _transport = _service(relation)

    with pytest.raises(RelationDataError, match="not safely editable"):
        await service.search_targets(RelationSearchParams(relation_id="broken"))
