from __future__ import annotations

import inspect
import json
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_query_schema_rpc import (
    _product_query_filter_operators,
)
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
from backend.contracts.schema_v2 import FieldDefinitionV2


def _formula_v2_field() -> dict[str, Any]:
    path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/field-definition.json"
    field = json.loads(path.read_text(encoding="utf-8"))
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {"language": "cel-v1", "source": "1 + 1", "resultType": "number"}
    return field


def _schema_v2_field(
    *, field_id: str, physical_name: str, display_name: str, logical_type: str
) -> dict[str, Any]:
    path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/field-definition.json"
    field = json.loads(path.read_text(encoding="utf-8"))
    field["identity"] = {
        "fieldId": field_id,
        "physicalName": physical_name,
        "providerFieldId": f"pb_{field_id.removeprefix('fld_')}",
    }
    field["displayName"] = display_name
    field["logicalType"] = logical_type
    storage_kind, display_kind = {
        "text": ("pocketbase-text", "text"),
        "editor": ("pocketbase-editor", "editor"),
        "number": ("pocketbase-number", "number"),
        "bool": ("pocketbase-bool", "bool"),
        "date": ("pocketbase-date", "date"),
        "dateTime": ("pocketbase-date", "dateTime"),
        "time": ("pocketbase-text", "time"),
        "autoDate": ("pocketbase-autodate", "readonly"),
        "email": ("pocketbase-email", "email"),
        "url": ("pocketbase-url", "url"),
        "select": ("pocketbase-select", "select"),
        "multiSelect": ("pocketbase-select", "select"),
        "relation": ("pocketbase-relation", "relation"),
        "file": ("pocketbase-file", "file"),
        "geoPoint": ("pocketbase-geo-point", "geoPoint"),
        "json": ("pocketbase-json", "json"),
        "formula": ("computed", "readonly"),
        "lookup": ("computed", "readonly"),
    }[logical_type]
    field["storage"]["kind"] = storage_kind
    field["display"]["kind"] = display_kind
    if logical_type in {"autoDate", "formula", "lookup"}:
        field["value"]["presence"] = {"mode": "computed"}
    elif logical_type in {"json"}:
        field["value"]["presence"] = {"mode": "native"}
    else:
        field["value"]["presence"] = {"mode": "companion"}
    if logical_type == "select":
        field["constraints"]["selection"]["max"] = 1
    elif logical_type == "json":
        field["storage"]["options"]["maxSize"] = 1024 * 1024
        field["display"]["mode"] = "code"
        field["display"]["indent"] = 2
    if logical_type == "relation":
        field["relation"] = {
            "targetTableId": "customers",
            "cardinality": "one",
            "deletePolicy": "restrict",
            "displayFieldId": "fld_00000002",
        }
    return field


def _schema_v2_snapshot(
    fields: list[dict[str, Any]], *, table_id: str = "orders", revision: str = "schema_4"
) -> dict[str, Any]:
    return {
        "contract": "vibetable.schema.v2",
        "tableId": table_id,
        "displayName": table_id.title(),
        "kind": "base",
        "schemaRevision": revision,
        "dataRevision": 1,
        "archivePolicy": {"mode": "none", "fieldId": None, "archivedValue": None},
        "fields": fields,
        "capabilities": [],
    }


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


def _service(responses: list[Any]) -> tuple[PocketBaseProductRpc, FakeTransport]:
    transport = FakeTransport(responses)
    client = PocketBaseClient(transport=transport, session_secret="a" * 64)
    return (
        PocketBaseProductRpc(
            client=client,
            transport=transport,
            session_secret="a" * 64,
        ),
        transport,
    )


def test_params_reject_credentials_recursively() -> None:
    with pytest.raises(ValidationError):
        ProductParams.model_validate({"nested": {"sessionSecret": "secret"}})


def test_root_adapter_exposes_only_the_closed_invoke_interface() -> None:
    public_async_methods = {
        name
        for name, member in inspect.getmembers(PocketBaseProductRpc, inspect.iscoroutinefunction)
        if not name.startswith("_")
    }

    assert public_async_methods == {"invoke"}


@pytest.mark.asyncio
async def test_invoke_dispatches_registered_method_through_closed_adapter_seam() -> None:
    service, transport = _service([{"tables": []}])
    params = PRODUCT_RPC_REGISTRY["schema.list"].model_validate({})

    result = await service.invoke("schema.list", params)

    assert result == {"tables": []}
    assert transport.requests[0]["path"] == "/api/vibetable/v2/schema/tables"


def test_schema_v2_plan_params_defer_domain_validation_but_keep_transport_closed() -> None:
    accepted = PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
        {
            "action": "create",
            "tableId": "orders",
            "fieldId": "",
            "expectedSchemaRevision": "schema_0001",
            "expectedDataRevision": None,
            "draft": {},
            "actor": {"id": "local-user", "kind": "user"},
            "conversionRule": "",
            "confirmation": "",
            "backupReceipt": "",
            "relationPair": {
                "reciprocalDisplayName": "订单",
                "reciprocalCardinality": "many",
                "sourceDisplayFieldId": "fld_order_number",
            },
        }
    )
    assert accepted.root["draft"] == {}
    assert accepted.root["relationPair"]["sourceDisplayFieldId"] == "fld_order_number"

    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
            {
                "action": "create",
                "tableId": "orders",
                "fieldId": "",
                "expectedSchemaRevision": "schema_0001",
                "expectedDataRevision": None,
                "draft": {},
                "actor": {"id": "local-user", "kind": "user"},
                "conversionRule": "",
                "confirmation": "",
                "backupReceipt": "",
                "providerUrl": "http://127.0.0.1:8090",
            }
        )


def test_schema_v2_apply_params_reject_open_nested_objects() -> None:
    with pytest.raises(ValidationError):
        PRODUCT_RPC_REGISTRY["field.change.apply"].model_validate(
            {
                "planId": "plan_1",
                "planHash": "a" * 64,
                "operationId": "op_1",
                "actor": {"id": "local-user", "kind": "user", "admin": True},
                "confirmations": [],
            }
        )


@pytest.mark.asyncio
async def test_field_settings_methods_use_only_frozen_v2_routes() -> None:
    service, transport = _service(
        [
            {"contract": "vibetable.schema.v2", "definition": None},
            {"contract": "vibetable.schema.v2", "planId": "plan_1"},
            {"contract": "vibetable.schema.v2", "operationId": "op_1"},
            {"contract": "vibetable.schema.v2", "fields": []},
        ]
    )
    describe = PRODUCT_RPC_REGISTRY["field.settings.describe"].model_validate(
        {"tableId": "orders", "fieldId": "fld_12345678"}
    )
    plan = PRODUCT_RPC_REGISTRY["field.change.plan"].model_validate(
        {
            "action": "retire",
            "tableId": "orders",
            "fieldId": "fld_12345678",
            "expectedSchemaRevision": "schema_0001",
            "expectedDataRevision": None,
            "draft": None,
            "actor": {"id": "local-user", "kind": "user"},
            "conversionRule": "",
            "confirmation": "",
            "backupReceipt": "",
            "relationPair": {
                "reciprocalDisplayName": "订单",
                "reciprocalCardinality": "many",
                "sourceDisplayFieldId": "fld_order_number",
            },
        }
    )
    apply = PRODUCT_RPC_REGISTRY["field.change.apply"].model_validate(
        {
            "planId": "plan_1",
            "planHash": "a" * 64,
            "operationId": "op_1",
            "actor": {"id": "local-user", "kind": "user"},
            "confirmations": [],
        }
    )
    recycle = PRODUCT_RPC_REGISTRY["field.recycleBin.list"].model_validate({"tableId": "orders"})

    await service.invoke("field.settings.describe", describe)
    await service.invoke("field.change.plan", plan)
    await service.invoke("field.change.apply", apply)
    await service.invoke("field.recycleBin.list", recycle)

    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/field-settings/orders",
        "/api/vibetable/v2/field-change/plan",
        "/api/vibetable/v2/field-change/apply",
        "/api/vibetable/v2/field-recycle-bin/orders",
    ]
    assert transport.requests[1]["json_body"]["relationPair"] == {
        "reciprocalDisplayName": "订单",
        "reciprocalCardinality": "many",
        "sourceDisplayFieldId": "fld_order_number",
    }


@pytest.mark.asyncio
async def test_schema_description_rejects_path_and_query_delimiters() -> None:
    service, transport = _service([])

    for table_id in ("../orders", "orders?admin=true", "orders#fragment", "%2fadmin"):
        with pytest.raises(ValueError, match="table id is invalid"):
            await service.invoke(
                "schema.describe",
                ProductParams.model_validate(
                    {
                        "collection": table_id,
                        "requestGeneration": 1,
                        "accepts": [
                            "vibetable.relation-capabilities.v1",
                            "vibetable.lookup-query.v1",
                        ],
                    }
                ),
            )

    assert transport.requests == []


@pytest.mark.parametrize("logical_type", ["editor", "email", "url"])
def test_product_query_text_family_uses_portable_text_operators(logical_type: str) -> None:
    assert _product_query_filter_operators({"logicalType": logical_type}) == [
        "eq",
        "ne",
        "in",
        "contains",
        "starts_with",
        "ends_with",
        "is_null",
        "is_not_null",
    ]


@pytest.mark.parametrize("logical_type", ["dateTime", "autoDate"])
def test_product_query_date_family_uses_ordered_operators(logical_type: str) -> None:
    assert _product_query_filter_operators({"logicalType": logical_type}) == [
        "eq",
        "ne",
        "in",
        "gt",
        "lt",
        "gte",
        "lte",
        "between",
        "is_null",
        "is_not_null",
    ]


def test_product_query_geo_point_uses_json_operators() -> None:
    assert _product_query_filter_operators({"logicalType": "geoPoint"}) == [
        "contains",
        "is_null",
        "is_not_null",
    ]


@pytest.mark.asyncio
async def test_schema_description_adapts_filter_operators_to_query_execution_types() -> None:
    formula = _formula_v2_field()
    formula["identity"] = {
        "fieldId": "fld_formula1",
        "physicalName": "f_formula1",
        "providerFieldId": "pb_formula1",
    }
    formula["displayName"] = "Score"
    lookup = _schema_v2_field(
        field_id="fld_lookup01",
        physical_name="f_lookup01",
        display_name="Lookup",
        logical_type="lookup",
    )
    lookup["value"]["presence"] = {"mode": "computed"}
    lookup["storage"]["kind"] = "computed"
    lookup["display"]["kind"] = "readonly"
    lookup["lookup"] = {
        "path": [{"relationFieldId": "fld_customer"}],
        "targetFieldId": "fld_metric01",
    }
    many_relation = _schema_v2_field(
        field_id="fld_customers",
        physical_name="f_customers",
        display_name="Customers",
        logical_type="relation",
    )
    many_relation["relation"]["cardinality"] = "many"
    many_lookup = _schema_v2_field(
        field_id="fld_manylook",
        physical_name="f_manylook",
        display_name="Many lookup",
        logical_type="lookup",
    )
    many_lookup["value"]["presence"] = {"mode": "computed"}
    many_lookup["storage"]["kind"] = "computed"
    many_lookup["display"]["kind"] = "readonly"
    many_lookup["lookup"] = {
        "path": [{"relationFieldId": "fld_customers"}],
        "targetFieldId": "fld_metric01",
    }
    select = _schema_v2_field(
        field_id="fld_status01",
        physical_name="f_status01",
        display_name="Status",
        logical_type="select",
    )
    select["select"] = {
        "options": [
            {
                "optionId": "opt_active01",
                "label": "Active",
                "color": "green",
                "order": 0,
                "state": "active",
            }
        ]
    }
    multi_select = _schema_v2_field(
        field_id="fld_tags0001",
        physical_name="f_tags0001",
        display_name="Tags",
        logical_type="multiSelect",
    )
    multi_select["select"] = select["select"]
    json_field = _schema_v2_field(
        field_id="fld_metadata",
        physical_name="f_metadata",
        display_name="Metadata",
        logical_type="json",
    )
    json_field["json"] = {
        "rootType": "object",
        "maxSize": 65536,
        "schema": {},
    }
    file_field = _schema_v2_field(
        field_id="fld_file0001",
        physical_name="f_file0001",
        display_name="Files",
        logical_type="file",
    )
    file_field["file"] = {
        "maxFiles": 1,
        "maxBytesPerFile": 1048576,
        "allowedMimeTypes": [],
        "thumbs": [],
        "protected": False,
    }
    definition = _schema_v2_snapshot(
        [
            _schema_v2_field(
                field_id="fld_region01",
                physical_name="f_region01",
                display_name="Region",
                logical_type="text",
            ),
            _schema_v2_field(
                field_id="fld_amount01",
                physical_name="f_amount01",
                display_name="Amount",
                logical_type="number",
            ),
            _schema_v2_field(
                field_id="fld_due_date",
                physical_name="f_due_date",
                display_name="Due date",
                logical_type="date",
            ),
            _schema_v2_field(
                field_id="fld_time0001",
                physical_name="f_time0001",
                display_name="Time",
                logical_type="time",
            ),
            select,
            multi_select,
            _schema_v2_field(
                field_id="fld_active01",
                physical_name="f_active01",
                display_name="Active",
                logical_type="bool",
            ),
            json_field,
            file_field,
            _schema_v2_field(
                field_id="fld_customer",
                physical_name="f_customer",
                display_name="Customer",
                logical_type="relation",
            ),
            many_relation,
            lookup,
            many_lookup,
            formula,
        ]
    )
    target_definition = _schema_v2_snapshot(
        [
            _schema_v2_field(
                field_id="fld_metric01",
                physical_name="f_metric01",
                display_name="Metric",
                logical_type="number",
            )
        ],
        table_id="customers",
    )
    for field in [*definition["fields"], *target_definition["fields"]]:
        FieldDefinitionV2.model_validate(field)
    capability_path = Path(__file__).parents[3] / "contracts/schema-v2/fixtures/capability.json"
    capability_template = json.loads(capability_path.read_text(encoding="utf-8"))
    definition["capabilities"] = []
    for logical_type in (
        "text",
        "number",
        "date",
        "time",
        "select",
        "multiSelect",
        "bool",
        "json",
        "file",
        "relation",
        "lookup",
        "formula",
    ):
        capability = dict(capability_template)
        capability["logicalType"] = logical_type
        capability["filterOperators"] = ["eq", "ne", "isEmpty", "isNotEmpty"]
        definition["capabilities"].append(capability)
    service, _ = _service(
        [
            definition,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "lookupMaxDepth": 8,
                "relations": [],
                "lookups": [
                    {
                        "lookupId": "orders.fld_lookup01",
                        "tableId": "orders",
                        "fieldId": "fld_lookup01",
                        "physicalName": "f_lookup01",
                        "displayName": "Lookup",
                        "relationFieldId": "fld_customer",
                        "path": [{"relationId": "orders.fld_customer"}],
                        "targetFieldId": "fld_metric01",
                        "resultCardinality": "one",
                        "outputStorage": "decimal",
                        "revision": 1,
                    },
                    {
                        "lookupId": "orders.fld_manylook",
                        "tableId": "orders",
                        "fieldId": "fld_manylook",
                        "physicalName": "f_manylook",
                        "displayName": "Many lookup",
                        "relationFieldId": "fld_customers",
                        "path": [{"relationId": "orders.fld_customers"}],
                        "targetFieldId": "fld_metric01",
                        "resultCardinality": "many",
                        "outputStorage": "decimal",
                        "revision": 1,
                    },
                ],
            },
            target_definition,
        ]
    )

    described = await service.invoke(
        "schema.describe",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "requestGeneration": 1,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            }
        ),
    )

    operators = {
        column["fieldId"]: column["filterOperators"] for column in described["schema"]["columns"]
    }
    text_operators = [
        "eq",
        "ne",
        "in",
        "contains",
        "starts_with",
        "ends_with",
        "is_null",
        "is_not_null",
    ]
    ordered_operators = [
        "eq",
        "ne",
        "in",
        "gt",
        "lt",
        "gte",
        "lte",
        "between",
        "is_null",
        "is_not_null",
    ]
    scalar_operators = ["eq", "ne", "in", "is_null", "is_not_null"]
    json_operators = ["contains", "is_null", "is_not_null"]
    assert operators == {
        "id": text_operators,
        "fld_region01": text_operators,
        "fld_amount01": ordered_operators,
        "fld_due_date": ordered_operators,
        "fld_time0001": text_operators,
        "fld_status01": text_operators,
        "fld_tags0001": json_operators,
        "fld_active01": scalar_operators,
        "fld_metadata": json_operators,
        "fld_file0001": json_operators,
        "fld_customer": scalar_operators,
        "fld_customers": scalar_operators,
        "fld_lookup01": ordered_operators,
        "fld_manylook": json_operators,
        "fld_formula1": ordered_operators,
    }


@pytest.mark.asyncio
async def test_schema_formula_and_file_use_only_fixed_routes() -> None:
    service, transport = _service(
        [
            {
                "contract": "vibetable.schema.v2",
                "operationId": "operation-create-table-12345678",
                "tableId": "tbl_orders",
                "displayName": "订单",
                "schemaRevision": "schema_0001",
            },
            {
                "canonicalSource": 'relationSum(f_lines, "f_amount")',
                "resultType": "number",
                "dependencies": [],
                "relationAggregatePaths": ["f_lines.f_amount"],
            },
            {"values": {"subtotal": 12}},
            {"contractVersion": "2.0", "downloadCapability": "cap"},
        ]
    )

    await service.invoke(
        "schema.table.create",
        PRODUCT_RPC_REGISTRY["schema.table.create"].model_validate(
            {
                "displayName": "订单",
                "operationId": "operation-create-table-12345678",
                "actor": {"id": "desktop-host", "kind": "host"},
            }
        ),
    )
    inspected = await service.invoke(
        "formula.draft.validate",
        PRODUCT_RPC_REGISTRY["formula.draft.validate"].model_validate(
            {"tableId": "orders", "displaySource": "SUM({明细}.{金额})"}
        ),
    )
    await service.invoke(
        "formula.preview",
        ProductParams.model_validate(
            {"tableId": "orders", "field": _formula_v2_field(), "row": {}, "changedFieldIds": []}
        ),
    )
    token = await service.invoke(
        "file.token",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice.pdf",
            }
        ),
    )

    assert token["downloadCapability"] == "cap"
    assert inspected["resultType"] == "number"
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/schema/tables",
        "/api/vibetable/v1/formulas/draft/validate",
        "/api/vibetable/v1/formulas/preview",
        "/api/vibetable/v1/files/token",
    ]
    assert all(
        request["headers"] == {"X-VibeTable-Session": "a" * 64} for request in transport.requests
    )


@pytest.mark.asyncio
async def test_schema_delete_uses_fixed_route_and_revision_guard() -> None:
    service, transport = _service([{"deleted": True, "tableId": "orders"}])

    result = await service.invoke(
        "schema.delete",
        ProductParams.model_validate({"tableId": "orders", "expectedRevision": "schema_0002"}),
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

    await service.invoke("relation.previewDelta", params)

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

    result = await service.invoke(
        "events.reconcile",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "schemaRevision": "schema_0001",
                "dataRevision": "data_0001",
            }
        ),
    )

    assert result["action"] == "refresh-data"
    assert transport.requests[0]["path"] == "/api/vibetable/v1/events/reconcile"


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

    await service.invoke(
        "history.read",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "itemId": "order-1",
                "scope": "row",
                "limit": 20,
                "offset": 0,
                "actions": ["update", "restore"],
            }
        ),
    )
    await service.invoke(
        "history.previewRestore",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "itemId": "order-1",
                "targetRevision": "rev-1",
                "scope": "row",
            }
        ),
    )
    await service.invoke(
        "history.applyRestore",
        ProductParams.model_validate(
            {"collection": "orders", "itemId": "order-1", "token": "restore-token"}
        ),
    )
    await service.invoke(
        "file.list",
        ProductParams.model_validate(
            {"tableId": "orders", "recordId": "order-1", "fieldId": "invoice"}
        ),
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
                "contractVersion": "2.0",
                "status": "applied",
                "changeSetId": "change-1",
            }
        ]
    )

    result = await service.invoke(
        "file.applyHostChange",
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
        ),
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
                "contractVersion": "2.0",
                "downloadCapability": "opaque-capability",
            }
        ]
    )

    result = await service.invoke(
        "file.saveHostFile",
        ProductParams.model_validate(
            {
                "tableId": "orders",
                "recordId": "row-1",
                "fieldId": "invoice",
                "storedName": "invoice_abcd.pdf",
                "outputPath": r"C:\host-selected\invoice.pdf",
            }
        ),
    )

    assert result == {
        "contractVersion": "2.0",
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

    assert await service.invoke("schema.list", ProductParams.model_validate({})) == {"tables": []}
    assert await service.invoke(
        "query.readRows",
        ProductParams.model_validate({"tableId": "orders", "rowIds": ["row-1"]}),
    ) == {"rows": [{"id": "row-1"}]}
    await service.invoke(
        "query.validateSnapshot",
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
        ),
    )

    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/schema/tables",
        "/api/vibetable/v1/query",
        "/api/vibetable/v1/query/validate-snapshot",
    ]


@pytest.mark.asyncio
@pytest.mark.parametrize("output_storage", ["text", "datetime"])
async def test_relation_renderer_contracts_are_adapted_from_product_shapes(
    output_storage: str,
) -> None:
    definition = _schema_v2_snapshot(
        [
            _schema_v2_field(
                field_id="fld_customer1",
                physical_name="f_customer1",
                display_name="Customer",
                logical_type="relation",
            )
        ]
    )
    lookup = {
        "lookupId": "orders.customer_name",
        "tableId": "orders",
        "fieldId": "customer_name",
        "physicalName": "customer_name",
        "displayName": "Customer name",
        "relationFieldId": "customer",
        "targetFieldId": "name",
        "resultCardinality": "one",
        "outputStorage": output_storage,
        "revision": 3,
    }
    relation = {
        "relationId": "orders.customer",
        "sourceTableId": "orders",
        "sourceFieldId": "fld_customer1",
        "physicalName": "f_customer1",
        "targetTableId": "customers",
        "cardinality": "one",
        "deletePolicy": "restrict",
    }
    service, transport = _service(
        [
            definition,
            {
                "tableId": "orders",
                "schemaRevision": "schema_4",
                "lookupMaxDepth": 8,
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
                    }
                ],
                "result": [],
                "adds": 0,
                "removes": 0,
                "canApply": True,
            },
        ]
    )

    described = await service.invoke(
        "schema.describe",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "requestGeneration": 9,
                "accepts": [
                    "vibetable.relation-capabilities.v1",
                    "vibetable.lookup-query.v1",
                ],
            }
        ),
    )
    assert described["capabilities"]["lookupMaxDepth"] == 8
    listed = await service.invoke(
        "lookup.list",
        ProductParams.model_validate({"collection": "orders"}),
    )
    preview = await service.invoke(
        "relation.previewDelta",
        ProductParams.model_validate(
            {
                "relationId": "orders.customer",
                "sourceItemId": "order-1",
                "expectedSchemaRevision": "schema_4",
                "adds": [],
                "removes": [],
                "idempotencyKey": "rel-1",
            }
        ),
    )

    assert described["contract"] == "vibetable.schema-describe.v1"
    assert described["requestGeneration"] == 9
    assert described["schema"]["normalizedRelations"][0]["kind"] == "m2o"
    assert listed["collection"] == "orders"
    assert listed["definitions"][0]["outputType"] == output_storage
    assert "aggregation" not in listed["definitions"][0]
    assert listed["lookupRevision"] == described["schema"]["lookupRevision"]
    assert preview == {
        "delta": {
            "relationId": "orders.customer",
            "sourceItemId": "order-1",
            "expectedSchemaRevision": "schema_4",
            "adds": [],
            "removes": [],
            "idempotencyKey": "rel-1",
        },
        "current": [
            {
                "collection": "customers",
                "itemId": "customer-1",
                "label": "Ada",
                "secondaryLabel": None,
            }
        ],
        "diagnostics": [],
        "canApply": True,
    }
    assert [request["path"] for request in transport.requests] == [
        "/api/vibetable/v2/schema/tables/orders",
        "/api/vibetable/v1/relations/describe",
        "/api/vibetable/v1/lookups/describe",
        "/api/vibetable/v1/relations/preview-delta",
    ]


@pytest.mark.asyncio
async def test_lookup_value_page_maps_physical_field_ref_to_stable_field_id() -> None:
    lookup = {
        "lookupId": "orders.line_skus_id",
        "tableId": "orders",
        "fieldId": "line_skus_id",
        "physicalName": "line_skus",
        "displayName": "Line SKUs",
        "relationFieldId": "lines_id",
        "targetFieldId": "sku_id",
        "outputStorage": "json",
        "revision": 1,
    }
    catalog = {
        "tableId": "orders",
        "schemaRevision": "schema_7",
        "relations": [],
        "lookups": [lookup],
    }
    page = {
        "state": "ok",
        "value": ["SKU-001"],
        "provenance": [
            {
                "collection": "lines",
                "collectionLabel": "明细",
                "itemId": "line-1",
                "recordLabel": "SKU-001",
                "fieldId": "sku_id",
                "fieldLabel": "SKU",
                "value": "SKU-001",
            }
        ],
        "provenanceTotal": 10_001,
        "provenanceOffset": 100,
        "provenanceLimit": 100,
        "provenanceHasMore": True,
    }
    service, transport = _service([catalog, page])
    lookup_revision = "sha256:6bd7460cb0333244b51b9ba40a0f4ce61198cdf9f6012ad812152671fdbb329e"

    result = await service.invoke(
        "lookup.valuePage",
        ProductParams.model_validate(
            {
                "collection": "orders",
                "fieldRef": "line_skus",
                "sourceRecordId": "order-1",
                "offset": 100,
                "limit": 100,
                "schemaRevision": "schema_7",
                "permissionRevision": "schema_7",
                "lookupRevision": lookup_revision,
            }
        ),
    )

    assert result["provenanceTotal"] == 10_001
    assert transport.requests[-1]["path"] == "/api/vibetable/v1/lookups/value-page"
    assert transport.requests[-1]["json_body"] == {
        "tableId": "orders",
        "schemaRevision": "schema_7",
        "sourceRecordId": "order-1",
        "fieldId": "line_skus_id",
        "offset": 100,
        "limit": 100,
    }
