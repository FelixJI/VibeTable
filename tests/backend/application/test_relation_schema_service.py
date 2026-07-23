from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.profile import JunctionProfile
from backend.application.relation_schema_service import (
    RelationSchemaError,
    RelationSchemaService,
)
from backend.contracts.lookup import LookupDefinition
from backend.contracts.relation_admin import (
    ApplyRelationChangeParams,
    CreateM2AConfig,
    CreateM2MConfig,
    CreateM2OConfig,
    CreateO2MConfig,
    JunctionContextFieldConfig,
    NormalizedRelationDescriptor,
    PreviewRelationChangeParams,
    SchemaSnapshot,
)


def _field(
    collection: str, field: str, *, pk: bool = False, note: str | None = None
) -> dict[str, Any]:
    return {
        "collection": collection,
        "field": field,
        "type": "uuid",
        "schema": {"data_type": "uuid", "is_primary_key": pk, "is_nullable": not pk},
        "meta": {"note": note} if note else {},
    }


class _Auth:
    async def access_token(self) -> str:
        return "token"


class _Client:
    def __init__(self) -> None:
        self.fields = [
            _field("orders", "id", pk=True),
            _field("contracts", "id", pk=True),
            _field("tags", "id", pk=True),
            _field("order_lines", "id", pk=True),
            _field("directus_files", "id", pk=True),
        ]
        self.relations: list[dict[str, Any]] = []

    async def schema_fields(self) -> list[dict[str, Any]]:
        return self.fields

    async def schema_relations(self) -> list[dict[str, Any]]:
        return self.relations


class _Transport:
    def __init__(self, client: _Client) -> None:
        self.client = client
        self.calls: list[tuple[str, str]] = []
        self.operations: dict[str, dict[str, Any]] = {}
        self.fail_relation_once = False

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.calls.append((method, path))
        if path == "/items/vibetable_schema_operations":
            if method == "GET":
                operation_id = kwargs["query"]["filter"]["operation_id"]["_eq"]
                row = self.operations.get(operation_id)
                return {"data": [] if row is None else [row]}
            if method == "POST":
                row = dict(kwargs["json_body"])
                self.operations[row["operation_id"]] = row
                return {"data": row}
            operation_id = kwargs["query"]["filter"]["operation_id"]["_eq"]
            self.operations[operation_id].update(kwargs["json_body"])
            return {"data": [self.operations[operation_id]]}
        if method == "POST" and path == "/fields/orders":
            body = kwargs["json_body"]
            self.client.fields.append(
                {
                    "collection": "orders",
                    "field": body["field"],
                    "type": body["type"],
                    "schema": body["schema"],
                    "meta": body["meta"],
                }
            )
            return {"data": body}
        if method == "POST" and path == "/relations":
            if self.fail_relation_once:
                self.fail_relation_once = False
                raise RuntimeError("simulated relation create interruption")
            body = kwargs["json_body"]
            self.client.relations.append(body)
            return {"data": body}
        if method == "PATCH" and path.startswith("/fields/"):
            _, _, collection, field = path.split("/")
            target = next(
                item
                for item in self.client.fields
                if item["collection"] == collection and item["field"] == field
            )
            for key in ("meta", "schema"):
                if key in kwargs["json_body"]:
                    target.setdefault(key, {}).update(kwargs["json_body"][key])
            return {"data": target}
        if method == "PATCH" and path.startswith("/relations/"):
            _, _, collection, field = path.split("/")
            target = next(
                item
                for item in self.client.relations
                if item["collection"] == collection and item["field"] == field
            )
            target.setdefault("schema", {}).update(kwargs["json_body"]["schema"])
            return {"data": target}
        if method == "DELETE" and path.startswith("/relations/"):
            _, _, collection, field = path.split("/")
            self.client.relations = [
                item
                for item in self.client.relations
                if not (item["collection"] == collection and item["field"] == field)
            ]
            return None
        if method == "DELETE" and path.startswith("/fields/"):
            _, _, collection, field = path.split("/")
            self.client.fields = [
                item
                for item in self.client.fields
                if not (item["collection"] == collection and item["field"] == field)
            ]
            return None
        raise AssertionError((method, path, kwargs))


def _snapshot(client: _Client) -> SchemaSnapshot:
    from backend.adapters.directus.relation_schema import normalize_directus_relations

    discovered = normalize_directus_relations(fields=client.fields, relations=client.relations)
    return SchemaSnapshot(
        collection="orders",
        primary_key="id",
        columns=[],
        normalized_relations=[
            relation for relation in discovered.relations if relation.source_collection == "orders"
        ],
        schema_revision="schema-2" if client.relations else "schema-1",
        permission_revision="permission-1",
        capability_hash="capability-1",
        lookup_revision="lookup-1",
    )


def _service() -> tuple[RelationSchemaService, _Client, _Transport]:
    client = _Client()
    transport = _Transport(client)

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return _snapshot(client)

    return (
        RelationSchemaService(
            client=client,
            transport=transport,
            auth=_Auth(),
            schema_provider=schema_provider,
        ),
        client,
        transport,
    )


@pytest.mark.asyncio
async def test_preview_is_zero_write_and_plans_directus_native_m2o() -> None:
    service, _client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="合同",
                related_collection="contracts",
                display_template="{{number}}",
            ),
        )
    )
    assert plan.can_apply is True
    assert [(step.resource, step.action) for step in plan.steps] == [
        ("field", "create"),
        ("relation", "create"),
    ]
    assert transport.calls == []


@pytest.mark.asyncio
async def test_file_preset_creates_directus_native_file_metadata() -> None:
    service, client, _transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="attachment",
                field_display_name="Attachment",
                related_collection="directus_files",
                preset="file",
            ),
        )
    )
    result = await service.apply(
        ApplyRelationChangeParams(
            plan_id=plan.plan_id,
            operation_id="file-preset",
            expected_schema_revision="schema-1",
        )
    )
    field = next(item for item in client.fields if item["field"] == "attachment")
    assert field["meta"]["special"] == ["file"]
    assert field["meta"]["interface"] == "file-image"
    assert result.relation is not None
    assert result.relation.preset == "file"


def test_relation_presets_reject_incompatible_shapes() -> None:
    with pytest.raises(ValueError, match="file preset"):
        CreateM2OConfig(
            field_key="attachment",
            field_display_name="Attachment",
            related_collection="contracts",
            preset="file",
        )
    with pytest.raises(ValueError, match="files preset"):
        CreateM2MConfig(
            field_key="attachments",
            field_display_name="Attachments",
            related_collection="contracts",
            preset="files",
            junction=JunctionProfile(
                collection="order_attachments",
                source_field="order",
                target_field="file",
            ),
        )


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("config", "expected_resources"),
    [
        (
            CreateO2MConfig(
                field_key="lines",
                field_display_name="明细",
                related_collection="order_lines",
                related_many_field="order",
            ),
            ["field", "field", "relation"],
        ),
        (
            CreateM2MConfig(
                field_key="tags",
                field_display_name="标签",
                related_collection="tags",
                junction=JunctionProfile(
                    collection="order_tags",
                    source_field="order",
                    target_field="tag",
                    context_fields=["weight"],
                ),
                junction_context_fields=[
                    JunctionContextFieldConfig(field="weight", type="decimal", nullable=True)
                ],
            ),
            ["collection", "field", "field", "field", "field", "relation", "relation"],
        ),
        (
            CreateM2AConfig(
                field_key="links",
                field_display_name="关联内容",
                allowed_collections=["contracts", "tags"],
                junction=JunctionProfile(
                    collection="order_links",
                    source_field="order",
                    target_field="item",
                    collection_field="collection",
                ),
            ),
            ["collection", "field", "field", "field", "field", "relation"],
        ),
    ],
)
async def test_preview_plans_o2m_m2m_and_m2a_native_shapes(
    config: Any, expected_resources: list[str]
) -> None:
    service, _client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=config,
        )
    )
    assert plan.can_apply is True
    assert [step.resource for step in plan.steps] == expected_resources
    assert transport.calls == []


@pytest.mark.asyncio
async def test_apply_records_journal_and_retry_does_not_duplicate_schema() -> None:
    service, client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="合同",
                related_collection="contracts",
            ),
        )
    )
    params = ApplyRelationChangeParams(
        plan_id=plan.plan_id,
        operation_id="operation-1",
        expected_schema_revision="schema-1",
    )
    first = await service.apply(params)
    second = await service.apply(params)

    assert first.relation is not None
    assert first.relation.managed is True
    assert second.relation == first.relation
    assert len([field for field in client.fields if field["field"] == "contract"]) == 1
    assert len(client.relations) == 1
    assert transport.operations["operation-1"]["status"] == "complete"
    field = next(item for item in client.fields if item["field"] == "contract")
    assert field["meta"]["translations"] == [{"language": "zh-CN", "translation": "\u5408\u540c"}]


@pytest.mark.asyncio
async def test_apply_rejects_live_schema_drift_before_first_write() -> None:
    service, client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="Contract",
                related_collection="contracts",
            ),
        )
    )
    client.relations.append(
        {
            "collection": "orders",
            "field": "unrelated",
            "related_collection": "tags",
            "schema": {"on_delete": "SET NULL"},
            "meta": {
                "many_collection": "orders",
                "many_field": "unrelated",
                "one_collection": "tags",
                "one_field": None,
            },
        }
    )

    with pytest.raises(RelationSchemaError, match="schema changed after preview"):
        await service.apply(
            ApplyRelationChangeParams(
                plan_id=plan.plan_id,
                operation_id="drifted-operation",
                expected_schema_revision="schema-1",
            )
        )
    assert "drifted-operation" not in transport.operations


@pytest.mark.asyncio
async def test_completed_operation_can_be_reconciled_after_process_restart() -> None:
    service, client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="Contract",
                related_collection="contracts",
            ),
        )
    )
    params = ApplyRelationChangeParams(
        plan_id=plan.plan_id,
        operation_id="restart-operation",
        expected_schema_revision="schema-1",
    )
    first = await service.apply(params)

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return _snapshot(client)

    restarted = RelationSchemaService(
        client=client,
        transport=transport,
        auth=_Auth(),
        schema_provider=schema_provider,
    )
    second = await restarted.apply(params)
    assert second.relation == first.relation
    assert len(client.relations) == 1


@pytest.mark.asyncio
async def test_partial_operation_resumes_from_persisted_private_plan() -> None:
    service, client, transport = _service()
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="Contract",
                related_collection="contracts",
            ),
        )
    )
    params = ApplyRelationChangeParams(
        plan_id=plan.plan_id,
        operation_id="partial-operation",
        expected_schema_revision="schema-1",
    )
    transport.fail_relation_once = True
    with pytest.raises(RuntimeError, match="interruption"):
        await service.apply(params)

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return _snapshot(client)

    restarted = RelationSchemaService(
        client=client,
        transport=transport,
        auth=_Auth(),
        schema_provider=schema_provider,
    )
    result = await restarted.apply(params)
    assert result.relation is not None
    assert len([field for field in client.fields if field["field"] == "contract"]) == 1
    assert len(client.relations) == 1


def test_m2m_update_applies_delete_policy_to_both_junction_relations() -> None:
    service, client, _transport = _service()
    junction = JunctionProfile(
        collection="order_tags",
        source_field="order",
        target_field="tag",
    )
    current = NormalizedRelationDescriptor(
        relation_id="orders.tags",
        field_ref="orders.tags",
        source_collection="orders",
        kind="m2m",
        related_collection="tags",
        junction=junction,
        managed=True,
    )
    config = CreateM2MConfig(
        field_key="tags",
        field_display_name="Tags",
        related_collection="tags",
        junction=junction,
        on_delete="restrict",
    )
    mutations, diagnostics = service._plan_update(current, config, client.fields)
    relation_steps = [
        mutation.step.key for mutation in mutations if mutation.step.resource == "relation"
    ]
    assert diagnostics == []
    assert relation_steps == [
        "relation:order_tags.order",
        "relation:order_tags.tag",
    ]


@pytest.mark.asyncio
async def test_delete_of_adopted_relation_is_blocked() -> None:
    service, client, _transport = _service()
    client.fields.append(_field("orders", "contract"))
    client.relations.append(
        {
            "collection": "orders",
            "field": "contract",
            "related_collection": "contracts",
            "schema": {"on_delete": "SET NULL"},
            "meta": {
                "many_collection": "orders",
                "many_field": "contract",
                "one_collection": "contracts",
                "one_field": None,
            },
        }
    )
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            action="delete",
            relation_id="orders.contract",
            expected_schema_revision="schema-2",
        )
    )
    assert plan.can_apply is False
    assert [item.code for item in plan.diagnostics] == ["external_relation_delete_blocked"]


@pytest.mark.asyncio
async def test_delete_managed_o2m_removes_alias_and_owned_many_field() -> None:
    service, client, _transport = _service()
    client.fields.extend(
        [
            {
                **_field(
                    "orders",
                    "lines",
                    note="[vibetable-managed-relation]",
                ),
                "type": "alias",
                "schema": None,
                "meta": {
                    "special": ["o2m"],
                    "interface": "list-o2m",
                    "note": "[vibetable-managed-relation]",
                },
            },
            _field(
                "order_lines",
                "order",
                note="[vibetable-managed-relation]",
            ),
        ]
    )
    client.relations.append(
        {
            "collection": "order_lines",
            "field": "order",
            "related_collection": "orders",
            "schema": {"on_delete": "SET NULL"},
            "meta": {
                "many_collection": "order_lines",
                "many_field": "order",
                "one_collection": "orders",
                "one_field": "lines",
            },
        }
    )
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            action="delete",
            relation_id="orders.lines",
            expected_schema_revision="schema-2",
        )
    )
    assert plan.can_apply is True
    assert [step.key for step in plan.steps] == [
        "relation:order_lines.order",
        "field:orders.lines",
        "field:order_lines.order",
    ]


@pytest.mark.asyncio
async def test_update_of_adopted_relation_is_blocked() -> None:
    service, client, _transport = _service()
    client.fields.append(_field("orders", "contract"))
    client.relations.append(
        {
            "collection": "orders",
            "field": "contract",
            "related_collection": "contracts",
            "schema": {"on_delete": "SET NULL"},
            "meta": {
                "many_collection": "orders",
                "many_field": "contract",
                "one_collection": "contracts",
                "one_field": None,
            },
        }
    )
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            action="update",
            relation_id="orders.contract",
            expected_schema_revision="schema-2",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="Contract",
                related_collection="contracts",
                unique=True,
            ),
        )
    )
    assert plan.can_apply is False
    assert plan.steps == []
    assert [item.code for item in plan.diagnostics] == ["external_relation_update_blocked"]


@pytest.mark.asyncio
async def test_update_replays_idempotent_field_and_relation_patches() -> None:
    service, client, transport = _service()
    create = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="合同",
                related_collection="contracts",
            ),
        )
    )
    await service.apply(
        ApplyRelationChangeParams(
            plan_id=create.plan_id,
            operation_id="create-operation",
            expected_schema_revision="schema-1",
        )
    )
    update = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            action="update",
            relation_id="orders.contract",
            expected_schema_revision="schema-2",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="合同",
                related_collection="contracts",
                nullable=False,
                unique=True,
                on_delete="restrict",
                display_template="{{number}}",
            ),
        )
    )
    assert [(step.resource, step.action) for step in update.steps] == [
        ("field", "update"),
        ("relation", "update"),
    ]
    await service.apply(
        ApplyRelationChangeParams(
            plan_id=update.plan_id,
            operation_id="update-operation",
            expected_schema_revision="schema-2",
        )
    )
    relation_field = next(item for item in client.fields if item["field"] == "contract")
    assert relation_field["schema"]["is_unique"] is True
    assert relation_field["schema"]["is_nullable"] is False
    assert client.relations[0]["schema"]["on_delete"] == "RESTRICT"
    assert ("PATCH", "/fields/orders/contract") in [call[:2] for call in transport.calls]


@pytest.mark.asyncio
async def test_delete_cascades_transitive_lookup_dependency_closure() -> None:
    base_service, client, transport = _service()
    create = await base_service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            expected_schema_revision="schema-1",
            config=CreateM2OConfig(
                field_key="contract",
                field_display_name="合同",
                related_collection="contracts",
            ),
        )
    )
    await base_service.apply(
        ApplyRelationChangeParams(
            plan_id=create.plan_id,
            operation_id="create-for-delete",
            expected_schema_revision="schema-1",
        )
    )
    direct = LookupDefinition.model_validate(
        {
            "lookupId": "contract-price",
            "collection": "orders",
            "fieldKey": "contract_price",
            "displayName": "合同价格",
            "path": [{"relationId": "orders.contract"}],
            "source": {"kind": "target_field", "fieldRef": "price"},
            "outputType": "decimal",
            "outputScale": 2,
        }
    )
    dependent = direct.model_copy(
        update={
            "lookup_id": "price-copy",
            "field_key": "price_copy",
            "source": {"kind": "lookup", "lookupId": "contract-price"},
            "dependencies": ["contract-price"],
        }
    )
    cascaded: list[str] = []

    async def schema_provider(_collection: str) -> SchemaSnapshot:
        return _snapshot(client)

    async def lookup_provider() -> list[LookupDefinition]:
        return [direct, LookupDefinition.model_validate(dependent)]

    async def lookup_cascade(ids: list[str], _operation_id: str) -> None:
        cascaded.extend(ids)

    service = RelationSchemaService(
        client=client,
        transport=transport,
        auth=_Auth(),
        schema_provider=schema_provider,
        lookup_provider=lookup_provider,
        lookup_cascade=lookup_cascade,
    )
    plan = await service.preview(
        PreviewRelationChangeParams(
            collection="orders",
            action="delete",
            relation_id="orders.contract",
            expected_schema_revision="schema-2",
        )
    )
    assert plan.affected_lookup_ids == ["contract-price", "price-copy"]
    assert plan.can_apply is True
    assert [(item.code, item.severity) for item in plan.diagnostics] == [
        ("lookup_dependency_exists", "warning")
    ]
    result = await service.apply(
        ApplyRelationChangeParams(
            plan_id=plan.plan_id,
            operation_id="delete-operation",
            expected_schema_revision="schema-2",
            cascade_lookup_ids=["contract-price", "price-copy"],
        )
    )
    assert result.deleted is True
    assert cascaded == ["contract-price", "price-copy"]
