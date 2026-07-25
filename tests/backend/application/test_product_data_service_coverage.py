from __future__ import annotations

import re
from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.application.product_data_service import (
    PRODUCT_PARAM_MODELS,
    PocketBaseProductDataService,
    ProductParams,
    _group_lookup_rows,
    _lookup_revision,
    _precision_scale,
    _renderer_lookup,
)


class ScriptedTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        assert self.responses, f"unexpected request: {method} {path}"
        return self.responses.pop(0)

    async def request_multipart(self, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": "MULTIPART", "path": path, **kwargs})
        assert self.responses, f"unexpected multipart request: {path}"
        return self.responses.pop(0)

    async def download_to_file(self, path: str, **kwargs: Any) -> int:
        self.requests.append({"method": "DOWNLOAD", "path": path, **kwargs})
        return 17


def service_with(
    responses: list[Any],
) -> tuple[PocketBaseProductDataService, ScriptedTransport]:
    transport = ScriptedTransport(responses)
    client = PocketBaseClient(transport=transport, session_secret="s" * 64)
    return (
        PocketBaseProductDataService(
            client=client,
            transport=transport,
            session_secret="s" * 64,
        ),
        transport,
    )


def scalar_field(
    field_id: str = "name",
    *,
    physical_name: str | None = None,
) -> dict[str, Any]:
    return {
        "fieldId": field_id,
        "physicalName": physical_name or field_id,
        "displayName": field_id.title(),
        "kind": "scalar",
        "dataType": "shortText",
        "storageType": "text",
        "nullable": True,
        "defaultValue": None,
        "constraints": [],
        "editor": {"kind": "text", "config": {}},
        "readOnly": False,
        "formula": None,
        "relation": None,
        "lookup": None,
        "attachmentPolicy": None,
    }


def relation_field(
    field_id: str = "customer",
    *,
    target: str = "customers",
    mode: str = "direct",
    junction_table: str | None = None,
    allowed: list[str] | None = None,
) -> dict[str, Any]:
    return {
        **scalar_field(field_id),
        "kind": "relation",
        "dataType": "relation",
        "storageType": "relation",
        "relation": {
            "mode": mode,
            "targetTableId": target,
            "cardinality": "many" if mode != "direct" else "one",
            "deletePolicy": "setNull",
            "junctionTableId": junction_table,
            "junctionSourceFieldId": "source",
            "junctionTargetFieldId": "target",
            "junctionDiscriminatorFieldId": "kind",
            "allowedTargetTableIds": allowed or [],
        },
    }


def table_schema(
    table_id: str,
    fields: list[dict[str, Any]],
    revision: str = "schema_3",
) -> dict[str, Any]:
    return {
        "contractVersion": "1.0",
        "tableId": table_id,
        "physicalName": table_id,
        "displayName": table_id.title(),
        "kind": "base",
        "schemaRevision": revision,
        "archivePolicy": {"mode": "none", "fieldId": None, "archivedValue": None},
        "fields": fields,
        "indexes": [],
    }


def lookup_renderer(
    *,
    revision: int = 1,
    aggregation: str = "single",
    output_type: str = "text",
) -> dict[str, Any]:
    return {
        "lookupId": "orders.customer_name",
        "collection": "orders",
        "fieldKey": "customer_name",
        "displayName": "Customer name",
        "path": [{"relationId": "orders.customer"}],
        "source": {"kind": "target_field", "fieldRef": "name"},
        "m2aFieldMapping": [],
        "aggregation": aggregation,
        "outputType": output_type,
        "revision": revision,
        "state": "valid",
        "diagnostics": [],
        "dependencies": [],
    }


def lookup_descriptor(revision: int = 1) -> dict[str, Any]:
    return {
        "lookupId": "orders.customer_name",
        "tableId": "orders",
        "fieldId": "customer_name",
        "physicalName": "customer_name",
        "displayName": "Customer name",
        "relationFieldId": "customer",
        "targetFieldId": "name",
        "aggregate": "first",
        "outputStorage": "text",
        "revision": revision,
    }


def page(rows: list[dict[str, Any]], *, offset: int = 0, limit: int = 50) -> dict[str, Any]:
    return {
        "rows": rows,
        "offset": offset,
        "limit": limit,
        "filteredRows": len(rows),
        "totalRows": len(rows),
        "querySnapshot": {"digest": "snapshot"},
    }


def test_product_params_enforce_json_size_depth_and_closed_shapes() -> None:
    with pytest.raises(ValidationError, match="must be an object"):
        ProductParams.model_validate([])
    with pytest.raises(ValidationError, match="forbidden field"):
        ProductParams.model_validate({1: "not-a-string-key"})
    with pytest.raises(ValidationError, match="JSON values"):
        ProductParams.model_validate({"value": float("nan")})
    with pytest.raises(ValidationError, match="safe size limit"):
        ProductParams.model_validate({"value": "x" * (1 << 20)})

    nested: dict[str, Any] = {}
    cursor = nested
    for _ in range(34):
        cursor["next"] = {}
        cursor = cursor["next"]
    with pytest.raises(ValidationError, match="too deeply nested"):
        ProductParams.model_validate(nested)

    schema_list = PRODUCT_PARAM_MODELS["schema.list"]
    assert schema_list.model_validate({}).root == {}
    with pytest.raises(ValidationError, match="unknown fields"):
        schema_list.model_validate({"extra": True})

    query_model = PRODUCT_PARAM_MODELS["query.page"]
    with pytest.raises(ValidationError, match="omit required fields"):
        query_model.model_validate({"tableId": "orders"})
    with pytest.raises(ValidationError, match="wrong type"):
        query_model.model_validate({"tableId": "orders", "query": []})
    with pytest.raises(ValidationError, match="must not be empty"):
        query_model.model_validate({"tableId": "", "query": {}})


@pytest.mark.asyncio
async def test_closed_routes_cover_query_mutation_formula_file_and_remove_only_attachment() -> None:
    service, transport = service_with(
        [
            {"definition": {"tableId": "orders"}},
            page([{"id": "row-1"}], limit=1),
            {"canApply": True, "operations": []},
            {"status": "applied", "receipt": {"id": "change-1"}},
            {"valid": True, "diagnostics": []},
            {"downloadCapability": "opaque", "contractVersion": "1.0"},
            {"status": "applied"},
            {"items": [{"tableId": "customers", "recordId": "c-1", "label": "Ada"}], "total": 1},
            {"current": [{"tableId": "customers", "recordId": "c-1", "label": "Ada"}]},
        ]
    )

    assert (
        await service.apply_schema(
            ProductParams.model_validate({"definition": {"tableId": "orders"}})
        )
    )["definition"]["tableId"] == "orders"
    queried = await service.query_page(
        ProductParams.model_validate({"tableId": "orders", "query": {"limit": 1}})
    )
    assert queried["rows"] == [{"id": "row-1"}]
    assert queried["snapshot"] == {"digest": "snapshot"}
    assert (await service.preview_mutation(ProductParams.model_validate({"operations": []})))[
        "canApply"
    ] is True
    assert (await service.apply_mutation(ProductParams.model_validate({"operations": []})))[
        "status"
    ] == "applied"
    assert (
        await service.validate_formula(ProductParams.model_validate({"source": "price * quantity"}))
    )["valid"] is True
    await service.create_file_token(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
                "variant": "thumb",
            }
        )
    )
    removed = await service.apply_host_attachment_change(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "schemaRevision": "schema_3",
                "expectedDigest": "sha256:" + "a" * 64,
                "hostPaths": [],
                "removeStoredNames": ["old.pdf"],
            }
        )
    )
    assert removed["status"] == "applied"

    searched = await service.search_relation_targets(
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "collection": "customers",
                "query": "ad",
                "offset": 0,
                "limit": 10,
            }
        )
    )
    assert searched["items"][0]["collection"] == "customers"
    applied = await service.apply_relation_delta(
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "row-1",
                "expectedSchemaRevision": "schema_3",
                "adds": [{"collection": "customers", "itemId": "c-1"}],
                "removes": [],
                "updates": [],
                "idempotencyKey": "relation-op-1",
            }
        )
    )
    assert applied["outcome"] == "committed"
    assert applied["current"][0]["itemId"] == "c-1"
    assert transport.responses == []


@pytest.mark.asyncio
async def test_route_validation_rejects_bad_rows_attachments_files_and_history() -> None:
    service, transport = service_with([])
    with pytest.raises(ValueError, match="rowIds"):
        await service.read_rows(ProductParams.model_validate({"tableId": "orders", "rowIds": [""]}))
    with pytest.raises(ValueError, match="does not accept"):
        await service.list_tables(ProductParams.model_validate({"unexpected": 1}))
    with pytest.raises(ValueError, match="variant must be a string"):
        await service.create_file_token(
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "storedName": "invoice.pdf",
                    "variant": 1,
                }
            )
        )
    for change in (
        {"hostPaths": [], "removeStoredNames": []},
        {"hostPaths": [""] * 33, "removeStoredNames": []},
    ):
        with pytest.raises(ValueError, match="attachment change"):
            await service.apply_host_attachment_change(
                ProductParams.model_validate(
                    {
                        "tableId": "orders",
                        "recordId": "row-1",
                        "fieldId": "invoice",
                        "schemaRevision": "schema_3",
                        "expectedDigest": "sha256:" + "a" * 64,
                        **change,
                    }
                )
            )
    with pytest.raises(ValueError, match="expectedDigest"):
        await service.apply_host_attachment_change(
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "schemaRevision": "schema_3",
                    "expectedDigest": "bad",
                    "hostPaths": [],
                    "removeStoredNames": ["old.pdf"],
                }
            )
        )
    with pytest.raises(ValueError, match="actions"):
        await service.read_history(
            ProductParams.model_validate({"collection": "orders", "actions": [""]})
        )
    with pytest.raises(ValueError, match="field"):
        await service.preview_history_restore(
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "itemId": "row-1",
                    "targetRevision": "rev-1",
                    "scope": "field",
                    "field": "",
                }
            )
        )
    with pytest.raises(ValueError, match="collection"):
        await service.search_relation_targets(
            ProductParams.model_validate({"relationId": "orders.customer", "collection": ""})
        )
    assert transport.requests == []


@pytest.mark.asyncio
async def test_lookup_create_update_delete_lifecycle_uses_normalized_schema() -> None:
    relation = relation_field()
    customers = table_schema("customers", [scalar_field()])
    descriptor_v1 = lookup_descriptor(1)
    descriptor_v2 = lookup_descriptor(2)
    existing_lookup = {
        **scalar_field("customer_name"),
        "physicalName": "customer_name",
        "displayName": "Customer name",
        "kind": "lookup",
        "dataType": "lookup",
        "readOnly": True,
        "lookup": {
            "relationFieldId": "customer",
            "path": [{"relationFieldId": "customer"}],
            "targetFieldId": "name",
            "junctionFieldId": "",
            "targetFieldIds": {},
            "aggregate": "first",
        },
    }
    source = table_schema("orders", [relation])
    source_with_lookup = table_schema("orders", [relation, existing_lookup], "schema_4")
    service, transport = service_with(
        [
            source,
            {"tableId": "orders", "schemaRevision": "schema_3", "relations": [], "lookups": []},
            customers,
            {"definition": source, "capabilities": {}},
            {"tableId": "orders", "schemaRevision": "schema_4", "lookups": [descriptor_v1]},
            source_with_lookup,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "relations": [],
                "lookups": [descriptor_v1],
            },
            customers,
            {"definition": source_with_lookup, "capabilities": {}},
            {"tableId": "orders", "schemaRevision": "schema_5", "lookups": [descriptor_v2]},
            source_with_lookup,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "relations": [],
                "lookups": [descriptor_v2],
            },
            {"definition": source, "capabilities": {}},
            {"tableId": "orders", "schemaRevision": "schema_5", "lookups": []},
        ]
    )

    created = await service.create_lookup(
        ProductParams.model_validate(
            {"definition": lookup_renderer(), "requestId": "lookup-create"}
        )
    )
    assert created["definition"]["lookupId"] == "orders.customer_name"

    updated_definition = lookup_renderer(revision=2, aggregation="distinct_values")
    updated = await service.update_lookup(
        ProductParams.model_validate(
            {
                "definition": updated_definition,
                "expectedRevision": 1,
                "requestId": "lookup-update",
            }
        )
    )
    assert updated["definition"]["revision"] == 2

    deleted = await service.delete_lookup(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "lookupId": "orders.customer_name",
                "expectedRevision": 2,
                "requestId": "lookup-delete",
            }
        )
    )
    assert deleted["deleted"] is True
    apply_requests = [
        request
        for request in transport.requests
        if request["path"] == "/api/vibetable/v1/schema/apply"
    ]
    assert len(apply_requests) == 3
    assert apply_requests[0]["json_body"]["definition"]["fields"][-1]["kind"] == "lookup"
    assert (
        apply_requests[1]["json_body"]["definition"]["fields"][-1]["lookup"]["aggregate"]
        == "distinct"
    )


@pytest.mark.asyncio
async def test_lookup_preview_and_grouped_query_return_renderer_envelopes() -> None:
    source = table_schema("orders", [relation_field()])
    customers = table_schema("customers", [scalar_field()])
    renderer = lookup_renderer()
    descriptor = lookup_descriptor()
    revision = _lookup_revision("schema_3", [descriptor])
    preview_rows = [
        {"id": "o-1", "customer_name": "Ada", "region": "EU"},
        {"id": "o-2", "customer_name": "Bob", "region": "US"},
    ]
    query_rows = [
        {"id": "o-1", "customer_name": "Ada", "region": "EU"},
        {"id": "o-2", "customer_name": "Bob", "region": "US"},
        {"id": "o-3", "customer_name": "Ana", "region": "EU"},
    ]
    service, transport = service_with(
        [
            source,
            customers,
            page(preview_rows),
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [],
                "lookups": [descriptor],
            },
            page(query_rows, limit=2),
            page(query_rows, limit=500),
        ]
    )

    preview = await service.preview_lookup(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "definitions": [renderer],
                "query": {
                    "offset": 0,
                    "limit": 50,
                    "groups": [{"fieldRef": "region", "direction": "asc"}],
                },
                "fieldRefs": ["customer_name"],
                "requestGeneration": 7,
                "schemaRevision": "schema_3",
                "permissionRevision": "schema_3",
                "lookupRevision": revision,
            }
        )
    )
    assert preview["columns"][0]["title"] == "Customer name"
    assert preview["groups"][0]["key"] == "EU"

    result = await service.query_lookups(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "fieldRefs": ["customer_name"],
                "query": {
                    "offset": 0,
                    "limit": 2,
                    "groups": [
                        {"fieldRef": "region", "direction": "desc"},
                        {"fieldRef": "customer_name", "direction": "asc"},
                    ],
                },
                "requestGeneration": 8,
                "schemaRevision": "schema_3",
                "permissionRevision": "schema_3",
                "lookupRevision": revision,
            }
        )
    )
    assert result["columns"][0]["outputType"] == "text"
    assert result["groups"][0]["key"] == "US"
    assert any(group["path"] == ["EU"] for group in result["groups"])
    assert transport.responses == []


@pytest.mark.asyncio
async def test_lookup_normalization_supports_junction_and_terminal_m2a_sources() -> None:
    junction_source = table_schema(
        "orders",
        [relation_field(mode="junction", junction_table="order_customers")],
    )
    junction_schema = table_schema("order_customers", [scalar_field("note")])
    m2a_source = table_schema(
        "orders",
        [
            relation_field(
                field_id="subject",
                target="customers",
                mode="m2a",
                allowed=["customers", "vendors"],
            )
        ],
    )
    customers = table_schema("customers", [scalar_field()])
    vendors = table_schema("vendors", [scalar_field()])
    service, transport = service_with([customers, junction_schema, customers, vendors])

    junction = await service._normalized_lookup_field(
        {
            **lookup_renderer(),
            "lookupId": "orders.relation_note",
            "fieldKey": "relation_note",
            "path": [{"relationId": "orders.customer"}],
            "source": {"kind": "junction_field", "fieldRef": "note"},
            "aggregation": "values",
            "outputType": "json",
        },
        junction_source,
    )
    assert junction["lookup"]["junctionFieldId"] == "note"
    assert junction["storageType"] == "json"

    polymorphic = await service._normalized_lookup_field(
        {
            **lookup_renderer(),
            "lookupId": "orders.subject_name",
            "fieldKey": "subject_name",
            "path": [{"relationId": "orders.subject"}],
            "m2aFieldMapping": [
                {"collection": "customers", "fieldRef": "name"},
                {"collection": "vendors", "fieldRef": "name"},
            ],
            "aggregation": "average",
            "outputType": "decimal",
            "outputScale": 4,
        },
        m2a_source,
    )
    assert polymorphic["lookup"]["targetFieldIds"] == {
        "customers": "name",
        "vendors": "name",
    }
    assert polymorphic["constraints"] == [{"kind": "precisionScale", "precision": 38, "scale": 4}]
    assert transport.responses == []


@pytest.mark.asyncio
async def test_single_relation_update_translates_current_and_desired_targets() -> None:
    descriptor = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "customer",
        "physicalName": "customer_id",
        "mode": "direct",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
    }
    service, transport = service_with(
        [
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [descriptor],
                "lookups": [],
            },
            {"rows": [{"id": "o-1", "customer_id": "c-old"}]},
            {"receipt": {"changeSetId": "change-1"}},
        ]
    )

    result = await service.update_single_relation(
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "o-1",
                "target": {
                    "collection": "customers",
                    "itemId": "c-new",
                    "label": "New customer",
                },
                "expectedSchemaRevision": "schema_3",
                "idempotencyKey": "single-relation-1",
                "expectedDigest": "sha256:" + "b" * 64,
            }
        )
    )

    assert result["outcome"] == "committed"
    assert result["current"]["itemId"] == "c-new"
    mutation = transport.requests[2]["json_body"]
    assert mutation["adds"] == [
        {"tableId": "customers", "recordId": "c-new", "label": "New customer"}
    ]
    assert mutation["removes"] == [{"tableId": "customers", "recordId": "c-old", "label": "c-old"}]
    assert mutation["actor"]["id"] == "local-user"
    assert transport.responses == []


@pytest.mark.asyncio
async def test_relation_delete_preview_removes_field_and_marks_dependent_lookup() -> None:
    relation = relation_field()
    source = table_schema("orders", [relation])
    descriptor = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "customer",
        "physicalName": "customer",
        "mode": "direct",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
    }
    service, transport = service_with(
        [
            source,
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [descriptor],
                "lookups": [lookup_descriptor()],
            },
            {"definition": source, "capabilities": {}},
        ]
    )

    preview = await service.preview_relation_change(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "action": "delete",
                "relationId": "orders.customer",
                "config": None,
                "expectedSchemaRevision": "schema_3",
            }
        )
    )

    assert preview["affectedLookupIds"] == ["orders.customer_name"]
    assert preview["steps"][-1] == {
        "resource": "lookup",
        "action": "delete",
        "key": "orders.customer_name",
        "destructive": True,
    }
    proposed = transport.requests[-1]["json_body"]["definition"]
    assert proposed["fields"] == []


@pytest.mark.asyncio
async def test_small_service_boundaries_cover_optional_and_invalid_catalog_paths() -> None:
    service, transport = service_with(
        [
            {"tableId": "orders", "schemaRevision": "schema_3"},
            {"valid": True},
            {"tables": "invalid"},
            {"tables": ["invalid"]},
            {
                "tableId": "orders",
                "schemaRevision": "schema_3",
                "relations": [],
                "lookups": [],
            },
        ]
    )
    assert (await service.get_table_schema(ProductParams.model_validate({"tableId": "orders"})))[
        "tableId"
    ] == "orders"
    await service.validate_snapshot(
        ProductParams.model_validate({"snapshot": {"digest": "x"}, "currentQuery": {"limit": 10}})
    )
    assert transport.requests[1]["json_body"]["currentQuery"] == {"limit": 10}

    with pytest.raises(ValueError, match="table catalog"):
        await service.list_table_ids()
    with pytest.raises(ValueError, match="table catalog"):
        await service.list_table_ids()
    assert await service.record_exists("", "row-1") is False
    with pytest.raises(ValueError, match="variant"):
        await service.save_attachment_to_host(
            ProductParams.model_validate(
                {
                    "tableId": "orders",
                    "recordId": "row-1",
                    "fieldId": "invoice",
                    "storedName": "invoice.pdf",
                    "outputPath": "invoice.pdf",
                    "variant": "",
                }
            )
        )
    with pytest.raises(ValueError, match=r"query\.groups"):
        await service.query_lookups(
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "fieldRefs": [],
                    "query": {"groups": "bad"},
                    "requestGeneration": 1,
                    "schemaRevision": "schema_3",
                    "permissionRevision": "schema_3",
                    "lookupRevision": "stale",
                }
            )
        )
    with pytest.raises(ValueError, match="revisions are stale"):
        await service.query_lookups(
            ProductParams.model_validate(
                {
                    "collection": "orders",
                    "fieldRefs": [],
                    "query": {},
                    "requestGeneration": 1,
                    "schemaRevision": "schema_2",
                    "permissionRevision": "schema_2",
                    "lookupRevision": "stale",
                }
            )
        )


def test_lookup_helpers_validate_grouping_rendering_and_precision() -> None:
    groups = _group_lookup_rows(
        [{"kind": "b"}, {"kind": "a"}, {"kind": "a"}],
        [{"fieldRef": "kind", "direction": "asc"}],
    )
    assert [(item["key"], item["count"]) for item in groups] == [("a", 2), ("b", 1)]
    with pytest.raises(ValueError, match="rows"):
        _group_lookup_rows("bad", [])
    with pytest.raises(ValueError, match="contain objects"):
        _group_lookup_rows([], ["bad"])
    with pytest.raises(ValueError, match="direction"):
        _group_lookup_rows([], [{"fieldRef": "kind", "direction": "sideways"}])

    assert _precision_scale([]) == (None, None)
    assert _precision_scale([{"kind": "precisionScale", "precision": 18, "scale": 2}]) == (18, 2)
    with pytest.raises(ValueError, match="field constraints"):
        _precision_scale(None)
    with pytest.raises(ValueError, match="precision constraints"):
        _precision_scale([{"kind": "precisionScale", "precision": True, "scale": 2}])

    descriptor = lookup_descriptor()
    assert _renderer_lookup(descriptor)["aggregation"] == "single"
    cases = (
        ({**descriptor, "aggregate": "median"}, "invalid Lookup aggregate"),
        ({**descriptor, "outputStorage": "bytes"}, "PocketBase returned an unknown data type"),
        ({**descriptor, "path": []}, "invalid Lookup path"),
        ({**descriptor, "path": ["bad"]}, "invalid Lookup path"),
        ({**descriptor, "junctionFieldId": ""}, "invalid junction Lookup field"),
        (
            {**descriptor, "targetFieldIds": {"customers": ""}},
            "invalid m2a Lookup mappings",
        ),
    )
    for invalid, expected in cases:
        with pytest.raises(ValueError, match=re.escape(expected)):
            _renderer_lookup(invalid)
