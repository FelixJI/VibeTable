from __future__ import annotations

from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.application.product_data_service import (
    PocketBaseProductDataService,
    ProductParams,
    _renderer_columns,
    _renderer_relation,
)


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        return self.responses.pop(0)

    async def request_multipart(self, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": "MULTIPART", "path": path, **kwargs})
        return self.responses.pop(0)

    async def download_to_file(self, path: str, **kwargs: Any) -> int:
        self.requests.append({"method": "DOWNLOAD", "path": path, **kwargs})
        return 12


def _service(responses: list[Any]) -> tuple[PocketBaseProductDataService, FakeTransport]:
    transport = FakeTransport(responses)
    client = PocketBaseClient(transport=transport, session_secret="a" * 64)
    return (
        PocketBaseProductDataService(
            client=client,
            transport=transport,
            session_secret="a" * 64,
        ),
        transport,
    )


def test_params_reject_credentials_recursively() -> None:
    with pytest.raises(ValidationError):
        ProductParams.model_validate({"nested": {"sessionSecret": "secret"}})


def test_renderer_columns_use_composite_relation_and_lookup_catalog_ids() -> None:
    columns = _renderer_columns(
        {
            "tableId": "orders",
            "fields": [
                {
                    "fieldId": "customer",
                    "physicalName": "customer",
                    "displayName": "Customer",
                    "kind": "relation",
                    "dataType": "relation",
                    "readOnly": False,
                    "nullable": True,
                    "constraints": [],
                },
                {
                    "fieldId": "customer_name",
                    "physicalName": "customer_name",
                    "displayName": "Customer name",
                    "kind": "lookup",
                    "dataType": "lookup",
                    "readOnly": True,
                    "nullable": True,
                    "constraints": [],
                },
            ],
        }
    )

    assert columns[1]["relationId"] == "orders.customer"
    assert columns[2]["lookupId"] == "orders.customer_name"


def test_renderer_columns_use_formula_result_type() -> None:
    columns = _renderer_columns(
        {
            "tableId": "items",
            "fields": [
                {
                    "fieldId": "doubled",
                    "physicalName": "doubled",
                    "displayName": "Doubled",
                    "kind": "formula",
                    "dataType": "formula",
                    "storageType": "number",
                    "readOnly": True,
                    "nullable": True,
                    "constraints": [],
                    "formula": {
                        "language": "cel-v1",
                        "source": "quantity * 2",
                        "resultType": "integer",
                    },
                }
            ],
        }
    )

    assert columns[1]["dataType"] == "integer"
    assert columns[1]["editable"] is False


def test_renderer_relation_accepts_sidecar_null_allowlist_and_schema_field_name() -> None:
    definition = {
        "tableId": "orders",
        "fields": [
            {
                "fieldId": "customer",
                "physicalName": "customer_record",
                "nullable": True,
            }
        ],
    }
    relation = _renderer_relation(
        {
            "relationId": "orders.customer",
            "sourceTableId": "orders",
            "sourceFieldId": "customer",
            # The field name belongs to the normalized table definition; older
            # catalogs did not duplicate it on the relation descriptor.
            "mode": "direct",
            "targetTableId": "customers",
            "cardinality": "one",
            "deletePolicy": "setNull",
            "junctionTableId": None,
            "allowedTargetTableIds": None,
        },
        definition,
    )

    assert relation["fieldRef"] == "customer_record"
    assert relation["manyField"] == "customer_record"
    assert relation["allowedCollections"] == ["customers"]
    assert relation["onDelete"] == "nullify"


@pytest.mark.asyncio
async def test_schema_description_rejects_path_and_query_delimiters() -> None:
    service, transport = _service([])

    for table_id in ("../orders", "orders?admin=true", "orders#fragment", "%2fadmin"):
        with pytest.raises(ValueError, match="table id is invalid"):
            await service.describe_schema(
                ProductParams.model_validate(
                    {
                        "collection": table_id,
                        "requestGeneration": 1,
                        "accepts": [
                            "vibetable.relation-capabilities.v1",
                            "vibetable.lookup-query.v1",
                        ],
                    }
                )
            )

    assert transport.requests == []


@pytest.mark.asyncio
async def test_schema_formula_and_file_use_only_fixed_routes() -> None:
    service, transport = _service(
        [
            {"definition": {}, "capabilities": {}},
            {"values": {"subtotal": 12}},
            {"contractVersion": "1.0", "downloadCapability": "cap"},
        ]
    )

    await service.validate_schema(
        ProductParams.model_validate({"definition": {}, "expectedRevision": 0})
    )
    await service.preview_formula(
        ProductParams.model_validate({"definition": {}, "row": {}, "changedFieldIds": []})
    )
    token = await service.create_file_token(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
            }
        )
    )

    assert token["downloadCapability"] == "cap"
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v1/schema/validate",
        "/api/vibetable/v1/formulas/preview",
        "/api/vibetable/v1/files/token",
    ]
    assert all(
        request["headers"] == {"X-VibeTable-Session": "a" * 64} for request in transport.requests
    )


@pytest.mark.asyncio
async def test_schema_delete_uses_fixed_route_and_revision_guard() -> None:
    service, transport = _service([{"deleted": True, "tableId": "orders"}])

    result = await service.delete_schema(
        ProductParams.model_validate({"tableId": "orders", "expectedRevision": "schema_0002"})
    )

    assert result == {"deleted": True, "tableId": "orders"}
    assert transport.requests[0]["method"] == "POST"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/schema/delete"
    assert transport.requests[0]["json_body"] == {
        "tableId": "orders",
        "expectedRevision": "schema_0002",
    }


@pytest.mark.asyncio
async def test_relation_delta_translates_legacy_target_names() -> None:
    service, transport = _service(
        [
            {
                "relationId": "orders.customer",
                "sourceRecordId": "order-1",
                "current": [],
                "result": [],
                "adds": 1,
                "removes": 0,
                "canApply": True,
            }
        ]
    )
    params = ProductParams.model_validate(
        {
            "relationId": "orders.customer",
            "sourceItemId": "order-1",
            "expectedSchemaRevision": "schema_0001",
            "adds": [
                {
                    "target": {
                        "collection": "customers",
                        "itemId": "customer-1",
                        "label": "Ada",
                    }
                }
            ],
            "updates": [],
            "removes": [],
            "idempotencyKey": "relation-1",
        }
    )

    await service.preview_relation_delta(params)

    assert transport.requests[0]["json_body"]["adds"] == [
        {"tableId": "customers", "recordId": "customer-1", "label": "Ada"}
    ]
    assert transport.requests[0]["json_body"]["actor"]["id"] == "local-user"


@pytest.mark.asyncio
async def test_reconcile_validates_sidecar_action() -> None:
    service, transport = _service(
        [
            {
                "tableId": "orders",
                "clientSchemaRevision": "schema_0001",
                "clientDataRevision": "data_0001",
                "currentSchemaRevision": "schema_0001",
                "currentDataRevision": "data_0002",
                "action": "refresh-data",
            }
        ]
    )

    result = await service.reconcile(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "schemaRevision": "schema_0001",
                "dataRevision": "data_0001",
            }
        )
    )

    assert result["action"] == "refresh-data"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/events/reconcile"


@pytest.mark.asyncio
async def test_workspace_authority_uses_product_table_catalog_and_query_port() -> None:
    service, transport = _service(
        [
            {"tables": [{"tableId": "orders"}, {"tableId": "customers"}]},
            {"rows": [{"id": "order-1"}]},
        ]
    )

    assert await service.list_table_ids() == ["orders", "customers"]
    assert await service.record_exists("orders", "order-1") is True
    assert transport.requests[0]["path"] == "/api/vibetable/v1/schema/tables"
    assert transport.requests[1]["json_body"] == {
        "operation": "readRows",
        "tableId": "orders",
        "rowIds": ["order-1"],
    }


@pytest.mark.asyncio
async def test_history_and_attachment_refs_use_closed_product_routes() -> None:
    service, transport = _service(
        [
            {"changeSets": [], "total": 0},
            {"token": "restore-token", "canApply": True},
            {"restoredToRevision": "rev-1"},
            {"attachments": []},
        ]
    )

    await service.read_history(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "itemId": "order-1",
                "scope": "row",
                "limit": 20,
                "offset": 0,
                "actions": ["update", "restore"],
            }
        )
    )
    await service.preview_history_restore(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "itemId": "order-1",
                "targetRevision": "rev-1",
                "scope": "row",
            }
        )
    )
    await service.apply_history_restore(
        ProductParams.model_validate(
            {"collection": "orders", "itemId": "order-1", "token": "restore-token"}
        )
    )
    await service.list_attachment_refs(
        ProductParams.model_validate(
            {"tableId": "orders", "recordId": "order-1", "fieldId": "invoice"}
        )
    )

    assert transport.requests[0]["path"] == "/api/vibetable/v1/history/change-sets"
    assert transport.requests[0]["query"]["action"] == ["update", "restore"]
    assert transport.requests[1]["path"] == "/api/vibetable/v1/history/restore-preview"
    assert transport.requests[2]["path"] == "/api/vibetable/v1/history/restore-apply"
    assert transport.requests[3]["path"].endswith("/attachments/refs")


@pytest.mark.asyncio
async def test_trusted_host_attachment_upload_uses_one_guarded_multipart_mutation() -> None:
    service, transport = _service(
        [
            {
                "contractVersion": "1.0",
                "status": "applied",
                "changeSetId": "change-1",
            }
        ]
    )

    result = await service.apply_host_attachment_change(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "schemaRevision": "schema_7",
                "expectedDigest": "sha256:" + "a" * 64,
                "hostPaths": [r"C:\host-selected\invoice.pdf"],
                "removeStoredNames": [],
            }
        )
    )

    request = transport.requests[0]
    assert result["status"] == "applied"
    assert request["method"] == "MULTIPART"
    assert request["path"] == "/api/vibetable/v1/mutations/apply"
    assert request["uploads"] == [("upload_0", r"C:\host-selected\invoice.pdf")]
    assert request["json_body"]["expectedDigest"] == "sha256:" + "a" * 64
    assert request["json_body"]["operations"] == [
        {
            "kind": "setAttachments",
            "recordId": "row-1",
            "fieldId": "invoice",
            "uploadHandles": ["upload_0"],
            "removeStoredNames": [],
        }
    ]


@pytest.mark.asyncio
async def test_trusted_host_attachment_download_keeps_capability_and_path_native() -> None:
    service, transport = _service(
        [
            {
                "contractVersion": "1.0",
                "downloadCapability": "opaque-capability",
            }
        ]
    )

    result = await service.save_attachment_to_host(
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice_abcd.pdf",
                "outputPath": r"C:\host-selected\invoice.pdf",
            }
        )
    )

    assert result == {
        "contractVersion": "1.0",
        "saved": True,
        "bytes": 12,
    }
    assert transport.requests[0]["path"] == "/api/vibetable/v1/files/token"
    assert transport.requests[1] == {
        "method": "DOWNLOAD",
        "path": "/api/vibetable/v1/attachments/download",
        "query": {"capability": "opaque-capability"},
        "target_path": r"C:\host-selected\invoice.pdf",
        "headers": {"X-VibeTable-Session": "a" * 64},
        "expected_status": (200,),
    }


@pytest.mark.asyncio
async def test_table_catalog_rows_and_snapshot_use_fixed_routes() -> None:
    service, transport = _service(
        [
            {"tables": []},
            {"rows": [{"id": "row-1"}]},
            {
                "valid": True,
                "currentDataRevision": 2,
                "currentSchemaRevision": "schema_2",
            },
        ]
    )

    assert await service.list_tables(ProductParams.model_validate({})) == {"tables": []}
    assert await service.read_rows(
        ProductParams.model_validate({"tableId": "orders", "rowIds": ["row-1"]})
    ) == {"rows": [{"id": "row-1"}]}
    await service.validate_snapshot(
        ProductParams.model_validate(
            {
                "snapshot": {
                    "snapshotId": "snap",
                    "digest": "a" * 64,
                    "databaseId": "local",
                    "table": "orders",
                    "schemaRevision": "schema_1",
                    "dataRevision": 1,
                    "normalizedQuery": {
                        "keyword": "",
                        "filters": [],
                        "sorts": [],
                        "offset": 0,
                        "limit": 100,
                    },
                }
            }
        )
    )

    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v1/schema/tables",
        "/api/vibetable/v1/query",
        "/api/vibetable/v1/query/validate-snapshot",
    ]


@pytest.mark.asyncio
async def test_relation_renderer_contracts_are_adapted_from_product_shapes() -> None:
    definition = {
        "contractVersion": "1.0",
        "tableId": "orders",
        "physicalName": "orders",
        "displayName": "Orders",
        "kind": "base",
        "schemaRevision": "schema_4",
        "archivePolicy": {
            "mode": "none",
            "fieldId": None,
            "archivedValue": None,
        },
        "fields": [
            {
                "fieldId": "customer",
                "physicalName": "customer",
                "displayName": "Customer",
                "kind": "relation",
                "dataType": "relation",
                "nullable": True,
                "constraints": [],
                "readOnly": False,
            }
        ],
        "indexes": [],
    }
    lookup = {
        "lookupId": "orders.customer_name",
        "tableId": "orders",
        "fieldId": "customer_name",
        "physicalName": "customer_name",
        "displayName": "Customer name",
        "relationFieldId": "customer",
        "targetFieldId": "name",
        "aggregate": "first",
        "outputStorage": "text",
        "revision": 3,
    }
    relation = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "customer",
        "physicalName": "customer",
        "mode": "direct",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "restrict",
        "allowedTargetTableIds": [],
    }
    service, transport = _service(
        [
            definition,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "relations": [relation],
                "lookups": [lookup],
            },
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "lookups": [lookup],
            },
            {
                "relationId": "orders.customer",
                "sourceRecordId": "order-1",
                "current": [
                    {
                        "tableId": "customers",
                        "recordId": "customer-1",
                        "label": "Ada",
                        "junctionValues": {},
                    }
                ],
                "result": [],
                "adds": 0,
                "removes": 0,
                "canApply": True,
            },
        ]
    )

    described = await service.describe_schema(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "requestGeneration": 9,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            }
        )
    )
    listed = await service.list_lookups(ProductParams.model_validate({"collection": "orders"}))
    preview = await service.preview_relation_delta(
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "order-1",
                "expectedSchemaRevision": "schema_4",
                "adds": [],
                "updates": [],
                "removes": [],
                "idempotencyKey": "rel-1",
            }
        )
    )

    assert described["contract"] == "vibetable.schema-describe.v1"
    assert described["requestGeneration"] == 9
    assert described["schema"]["normalizedRelations"][0]["kind"] == "m2o"
    assert listed["collection"] == "orders"
    assert listed["definitions"][0]["aggregation"] == "single"
    assert listed["lookupRevision"] == described["schema"]["lookupRevision"]
    assert preview == {
        "delta": {
            "relationId": "orders.customer",
            "sourceItemId": "order-1",
            "expectedSchemaRevision": "schema_4",
            "adds": [],
            "updates": [],
            "removes": [],
            "idempotencyKey": "rel-1",
        },
        "current": [
            {
                "collection": "customers",
                "itemId": "customer-1",
                "label": "Ada",
                "junctionId": None,
                "junctionRevision": None,
                "junctionValues": {},
            }
        ],
        "diagnostics": [],
        "canApply": True,
    }
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v1/schema/tables/orders",
        "/api/vibetable/v1/relations/describe",
        "/api/vibetable/v1/lookups/describe",
        "/api/vibetable/v1/relations/preview-delta",
    ]


@pytest.mark.asyncio
async def test_relation_admin_preview_apply_and_operation_replay_are_connected() -> None:
    definition = {
        "contractVersion": "1.0",
        "tableId": "orders",
        "physicalName": "orders",
        "displayName": "Orders",
        "kind": "base",
        "schemaRevision": "schema_4",
        "archivePolicy": {"mode": "none", "fieldId": None, "archivedValue": None},
        "fields": [],
        "indexes": [],
    }
    applied = {
        **definition,
        "schemaRevision": "schema_5",
        "fields": [
            {
                "fieldId": "customer",
                "physicalName": "customer",
                "displayName": "Customer",
                "kind": "relation",
                "dataType": "relation",
                "storageType": "relation",
                "nullable": True,
                "defaultValue": None,
                "constraints": [],
                "editor": {"kind": "relation", "config": {}},
                "readOnly": False,
                "formula": None,
                "relation": {
                    "mode": "direct",
                    "targetTableId": "customers",
                    "cardinality": "one",
                    "deletePolicy": "setNull",
                    "junctionTableId": None,
                    "junctionSourceFieldId": "",
                    "junctionTargetFieldId": "",
                    "junctionDiscriminatorFieldId": "",
                    "allowedTargetTableIds": [],
                },
                "lookup": None,
                "attachmentPolicy": None,
            }
        ],
    }
    relation = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "customer",
        "physicalName": "customer",
        "mode": "direct",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
        "allowedTargetTableIds": [],
    }
    service, transport = _service(
        [
            definition,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "relations": [],
                "lookups": [],
            },
            {"definition": definition, "capabilities": {}},
            applied,
            {
                "tableId": "orders",
                "schemaRevision": "schema_5",
                "relations": [relation],
                "lookups": [],
            },
        ]
    )
    preview = await service.preview_relation_change(
        ProductParams.model_validate(
            {
                "collection": "orders",
                "action": "create",
                "relationId": None,
                "config": {
                    "kind": "m2o",
                    "fieldKey": "customer",
                    "fieldDisplayName": "Customer",
                    "relatedCollection": "customers",
                    "unique": False,
                    "nullable": True,
                    "onDelete": "nullify",
                    "displayTemplate": None,
                    "preset": "standard",
                },
                "expectedSchemaRevision": "schema_4",
            }
        )
    )
    apply_params = ProductParams.model_validate(
        {
            "planId": preview["planId"],
            "operationId": "op-relation-1",
            "expectedSchemaRevision": "schema_4",
            "cascadeLookupIds": [],
        }
    )
    result = await service.apply_relation_change(apply_params)
    replay = await service.apply_relation_change(apply_params)

    assert result["relation"]["relationId"] == "orders.customer"
    assert result["schemaRevision"] == "schema_5"
    assert replay == result
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v1/schema/tables/orders",
        "/api/vibetable/v1/relations/describe",
        "/api/vibetable/v1/schema/validate",
        "/api/vibetable/v1/schema/apply",
        "/api/vibetable/v1/relations/describe",
    ]


@pytest.mark.asyncio
async def test_multihop_lookup_validation_persists_and_round_trips_path() -> None:
    def table(
        table_id: str,
        fields: list[dict[str, Any]],
    ) -> dict[str, Any]:
        return {
            "contractVersion": "1.0",
            "tableId": table_id,
            "physicalName": table_id,
            "displayName": table_id.title(),
            "kind": "base",
            "schemaRevision": "schema_1",
            "archivePolicy": {
                "mode": "none",
                "fieldId": None,
                "archivedValue": None,
            },
            "fields": fields,
            "indexes": [],
        }

    def relation(field_id: str, target: str) -> dict[str, Any]:
        return {
            "fieldId": field_id,
            "physicalName": field_id,
            "displayName": field_id.title(),
            "kind": "relation",
            "dataType": "relation",
            "storageType": "relation",
            "nullable": True,
            "defaultValue": None,
            "constraints": [],
            "editor": {"kind": "relation", "config": {}},
            "readOnly": False,
            "formula": None,
            "relation": {
                "mode": "direct",
                "targetTableId": target,
                "cardinality": "one",
                "deletePolicy": "setNull",
                "junctionTableId": None,
                "junctionSourceFieldId": "",
                "junctionTargetFieldId": "",
                "junctionDiscriminatorFieldId": "",
                "allowedTargetTableIds": [],
            },
            "lookup": None,
            "attachmentPolicy": None,
        }

    scalar = {
        "fieldId": "name_id",
        "physicalName": "name",
        "displayName": "Name",
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
    orders = table("orders", [relation("customer_id", "customers")])
    customers = table("customers", [relation("company_id", "companies")])
    companies = table("companies", [scalar])
    descriptor = {
        "lookupId": "orders.company_name_id",
        "tableId": "orders",
        "fieldId": "company_name_id",
        "physicalName": "company_name",
        "displayName": "Company name",
        "relationFieldId": "customer_id",
        "path": [
            {"relationId": "orders.customer_id"},
            {"relationId": "customers.company_id"},
        ],
        "targetFieldId": "name_id",
        "aggregate": "first",
        "outputStorage": "text",
        "revision": 1,
    }
    service, transport = _service(
        [
            orders,
            customers,
            companies,
            {"definition": orders, "capabilities": {}},
            {
                "tableId": "orders",
                "schemaRevision": "schema_1",
                "lookups": [descriptor],
            },
        ]
    )
    definition = {
        "lookupId": "orders.company_name_id",
        "collection": "orders",
        "fieldKey": "company_name",
        "displayName": "Company name",
        "path": [
            {"relationId": "orders.customer_id"},
            {"relationId": "customers.company_id"},
        ],
        "source": {"kind": "target_field", "fieldRef": "name"},
        "m2aFieldMapping": [],
        "aggregation": "single",
        "outputType": "text",
        "revision": 1,
        "state": "valid",
        "diagnostics": [],
        "dependencies": [],
    }

    validated = await service.validate_lookup(
        ProductParams.model_validate({"definition": definition, "existing": []})
    )
    listed = await service.list_lookups(ProductParams.model_validate({"collection": "orders"}))

    sent_lookup = transport.requests[3]["json_body"]["definition"]["fields"][-1]["lookup"]
    assert sent_lookup["relationFieldId"] == "customer_id"
    assert sent_lookup["path"] == [
        {"relationFieldId": "customer_id"},
        {"relationFieldId": "company_id"},
    ]
    assert validated["valid"] is True
    assert listed["definitions"][0]["path"] == definition["path"]
