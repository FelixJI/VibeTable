"""Closed JSON-RPC-to-sidecar product adapter.

This module is the only Python composition seam for renderer-reachable table
data operations.  Every public method selects a fixed product route; callers
cannot supply an HTTP path, provider credential, or arbitrary backend method.
"""

from __future__ import annotations

import hashlib
import json
import re
import uuid
from typing import Any, ClassVar

from pydantic import RootModel, model_validator

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseTransport
from backend.contracts.schema_v2 import ApplyRequestV2

_MAX_PARAMS_BYTES = 1 << 20
_MAX_DEPTH = 32
_PATH_SEGMENT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
_ROW_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
_SCHEMA_REVISION = re.compile(r"^schema_([0-9]+)$")
_FORBIDDEN_KEYS = {
    "accessToken",
    "legacyToken",
    "password",
    "pocketBaseToken",
    "refreshToken",
    "sessionSecret",
}


class ProductParams(RootModel[dict[str, Any]]):
    """JSON-only product parameters with credential and resource guards."""

    _allowed_fields: ClassVar[frozenset[str] | None] = None
    _required_fields: ClassVar[frozenset[str]] = frozenset()
    _field_types: ClassVar[dict[str, tuple[type, ...]]] = {}
    _catalog_example: ClassVar[dict[str, Any] | None] = None

    @model_validator(mode="before")
    @classmethod
    def validate_product_params(cls, value: Any) -> Any:
        if not isinstance(value, dict):
            raise ValueError("product params must be an object")
        _validate_value(value, 0)
        try:
            encoded = json.dumps(
                value,
                ensure_ascii=False,
                allow_nan=False,
                separators=(",", ":"),
            ).encode()
        except (TypeError, ValueError):
            raise ValueError("product params must contain JSON values") from None
        if len(encoded) > _MAX_PARAMS_BYTES:
            raise ValueError("product params exceed the safe size limit")
        return value

    @model_validator(mode="after")
    def validate_closed_method_shape(self) -> ProductParams:
        if self._allowed_fields is None:
            return self
        names = frozenset(self.root)
        unknown = names - self._allowed_fields
        if unknown:
            raise ValueError("product params contain unknown fields: " + ", ".join(sorted(unknown)))
        missing = self._required_fields - names
        if missing:
            raise ValueError("product params omit required fields: " + ", ".join(sorted(missing)))
        for name, expected in self._field_types.items():
            if name not in self.root:
                continue
            value = self.root[name]
            if isinstance(value, bool) and int in expected and bool not in expected:
                raise ValueError(f"{name} has the wrong type")
            if not isinstance(value, expected):
                raise ValueError(f"{name} has the wrong type")
            if str in expected and isinstance(value, str) and not value:
                raise ValueError(f"{name} must not be empty")
        return self


def _closed_params(
    name: str,
    *,
    allowed: tuple[str, ...],
    required: tuple[str, ...] = (),
    field_types: dict[str, tuple[type, ...]] | None = None,
) -> type[ProductParams]:
    """Create a named strict top-level DTO while retaining recursive guards."""
    return type(
        name,
        (ProductParams,),
        {
            "__module__": __name__,
            "_allowed_fields": frozenset(allowed),
            "_required_fields": frozenset(required),
            "_field_types": field_types or {},
        },
    )


class FieldChangePlanParams(ProductParams):
    """Guards the transport envelope; Go owns field-domain validation."""

    _allowed_fields = frozenset(
        {
            "action",
            "tableId",
            "fieldId",
            "expectedSchemaRevision",
            "expectedDataRevision",
            "draft",
            "actor",
            "conversionRule",
            "confirmation",
            "backupReceipt",
        }
    )
    _required_fields = _allowed_fields
    _field_types = {
        "draft": (dict, type(None)),
        "actor": (dict,),
    }

    _catalog_example = {
        "action": "retire",
        "tableId": "orders",
        "fieldId": "fld_example",
        "expectedSchemaRevision": "schema_1",
        "expectedDataRevision": None,
        "draft": None,
        "actor": {"id": "local-user", "kind": "user"},
        "conversionRule": "",
        "confirmation": "",
        "backupReceipt": "",
    }


class FieldChangeApplyParams(ProductParams):
    """Deeply validates the complete Schema v2 apply request."""

    _catalog_example = {
        "planId": "plan_example",
        "planHash": "a" * 64,
        "operationId": "operation_example",
        "actor": {"id": "local-user", "kind": "user"},
        "confirmations": [],
    }

    @model_validator(mode="after")
    def validate_apply(self) -> FieldChangeApplyParams:
        ApplyRequestV2.model_validate(self.root)
        return self


_MUTATION_FIELDS = (
    "contractVersion",
    "requestId",
    "idempotencyKey",
    "tableId",
    "schemaRevision",
    "operations",
    "actor",
    "expectedRevision",
    "expectedDigest",
)
_RELATION_DELTA_FIELDS = (
    "relationId",
    "sourceItemId",
    "expectedSchemaRevision",
    "expectedDateUpdated",
    "adds",
    "updates",
    "removes",
    "idempotencyKey",
)

# The dispatcher selects one of these method-specific DTOs.  This makes an
# unknown field, a missing required field, or a top-level type mismatch a
# JSON-RPC -32602 response before application code or HTTP is reached.
PRODUCT_PARAM_MODELS: dict[str, type[ProductParams]] = {
    "field.settings.describe": _closed_params(
        "FieldSettingsDescribeParams",
        allowed=("tableId", "fieldId"),
        required=("tableId",),
        field_types={"tableId": (str,), "fieldId": (str,)},
    ),
    "field.change.plan": FieldChangePlanParams,
    "field.change.apply": FieldChangeApplyParams,
    "field.change.status": _closed_params(
        "FieldChangeStatusParams",
        allowed=("jobId",),
        required=("jobId",),
        field_types={"jobId": (str,)},
    ),
    "field.change.cancel": _closed_params(
        "FieldChangeCancelParams",
        allowed=("jobId",),
        required=("jobId",),
        field_types={"jobId": (str,)},
    ),
    "field.recycleBin.list": _closed_params(
        "FieldRecycleBinParams",
        allowed=("tableId",),
        required=("tableId",),
        field_types={"tableId": (str,)},
    ),
    "schema.validate": _closed_params(
        "SchemaValidateParams",
        allowed=("definition", "expectedRevision"),
        required=("definition", "expectedRevision"),
        field_types={"definition": (dict,), "expectedRevision": (int,)},
    ),
    "schema.apply": _closed_params(
        "SchemaApplyParams",
        allowed=("definition", "expectedRevision", "operationId"),
        required=("definition", "expectedRevision"),
        field_types={
            "definition": (dict,),
            "expectedRevision": (int,),
            "operationId": (str,),
        },
    ),
    "schema.delete": _closed_params(
        "SchemaDeleteParams",
        allowed=("tableId", "expectedRevision"),
        required=("tableId", "expectedRevision"),
        field_types={"tableId": (str,), "expectedRevision": (str,)},
    ),
    "schema.list": _closed_params("SchemaListParams", allowed=()),
    "schema.getTable": _closed_params(
        "SchemaGetTableParams",
        allowed=("tableId",),
        required=("tableId",),
        field_types={"tableId": (str,)},
    ),
    "schema.describe": _closed_params(
        "SchemaDescribeParams",
        allowed=("collection", "requestGeneration", "accepts"),
        required=("collection", "requestGeneration", "accepts"),
        field_types={
            "collection": (str,),
            "requestGeneration": (int,),
            "accepts": (list,),
        },
    ),
    "query.page": _closed_params(
        "QueryPageParams",
        allowed=("tableId", "query"),
        required=("tableId", "query"),
        field_types={"tableId": (str,), "query": (dict,)},
    ),
    "query.view": _closed_params(
        "QueryViewParams",
        allowed=("tableId", "view"),
        required=("tableId", "view"),
        field_types={"tableId": (str,), "view": (dict,)},
    ),
    "query.readRows": _closed_params(
        "QueryReadRowsParams",
        allowed=("tableId", "rowIds"),
        required=("tableId", "rowIds"),
        field_types={"tableId": (str,), "rowIds": (list,)},
    ),
    "query.validateSnapshot": _closed_params(
        "QueryValidateSnapshotParams",
        allowed=("snapshot", "currentQuery"),
        required=("snapshot",),
        field_types={"snapshot": (dict,), "currentQuery": (dict,)},
    ),
    "mutation.preview": _closed_params(
        "MutationPreviewParams",
        allowed=_MUTATION_FIELDS,
        required=_MUTATION_FIELDS[:7],
        field_types={
            "contractVersion": (str,),
            "requestId": (str,),
            "idempotencyKey": (str,),
            "tableId": (str,),
            "schemaRevision": (str,),
            "operations": (list,),
            "actor": (dict,),
            "expectedRevision": (str, type(None)),
            "expectedDigest": (str, type(None)),
        },
    ),
    "mutation.apply": _closed_params(
        "MutationApplyParams",
        allowed=_MUTATION_FIELDS,
        required=_MUTATION_FIELDS[:7],
        field_types={
            "contractVersion": (str,),
            "requestId": (str,),
            "idempotencyKey": (str,),
            "tableId": (str,),
            "schemaRevision": (str,),
            "operations": (list,),
            "actor": (dict,),
            "expectedRevision": (str, type(None)),
            "expectedDigest": (str, type(None)),
        },
    ),
    "formula.validate": _closed_params(
        "FormulaValidateParams",
        allowed=("definition",),
        required=("definition",),
        field_types={"definition": (dict,)},
    ),
    "formula.draft.validate": _closed_params(
        "FormulaDraftValidateParams",
        allowed=("tableId", "displaySource"),
        required=("tableId", "displaySource"),
        field_types={"tableId": (str,), "displaySource": (str,)},
    ),
    "formula.preview": _closed_params(
        "FormulaPreviewParams",
        allowed=("definition", "row", "changedFieldIds"),
        required=("definition", "row", "changedFieldIds"),
        field_types={
            "definition": (dict,),
            "row": (dict,),
            "changedFieldIds": (list,),
        },
    ),
    "file.list": _closed_params(
        "FileListParams",
        allowed=("tableId", "recordId", "fieldId"),
        required=("tableId", "recordId", "fieldId"),
        field_types={
            "tableId": (str,),
            "recordId": (str,),
            "fieldId": (str,),
        },
    ),
    "file.token": _closed_params(
        "FileTokenParams",
        allowed=("tableId", "recordId", "fieldId", "storedName", "variant"),
        required=("tableId", "recordId", "fieldId", "storedName"),
        field_types={
            "tableId": (str,),
            "recordId": (str,),
            "fieldId": (str,),
            "storedName": (str,),
            "variant": (str,),
        },
    ),
    # Trusted-host-only methods. They are registered on the private pipe but
    # deliberately absent from the renderer ProductDataRpcRegistry.
    "file.applyHostChange": _closed_params(
        "FileApplyHostChangeParams",
        allowed=(
            "tableId",
            "recordId",
            "fieldId",
            "schemaRevision",
            "expectedDigest",
            "hostPaths",
            "removeStoredNames",
        ),
        required=(
            "tableId",
            "recordId",
            "fieldId",
            "schemaRevision",
            "expectedDigest",
            "hostPaths",
            "removeStoredNames",
        ),
        field_types={
            "tableId": (str,),
            "recordId": (str,),
            "fieldId": (str,),
            "schemaRevision": (str,),
            "expectedDigest": (str,),
            "hostPaths": (list,),
            "removeStoredNames": (list,),
        },
    ),
    "file.saveHostFile": _closed_params(
        "FileSaveHostFileParams",
        allowed=(
            "tableId",
            "recordId",
            "fieldId",
            "storedName",
            "variant",
            "outputPath",
        ),
        required=(
            "tableId",
            "recordId",
            "fieldId",
            "storedName",
            "outputPath",
        ),
        field_types={
            "tableId": (str,),
            "recordId": (str,),
            "fieldId": (str,),
            "storedName": (str,),
            "variant": (str,),
            "outputPath": (str,),
        },
    ),
    "events.reconcile": _closed_params(
        "EventsReconcileParams",
        allowed=("tableId", "schemaRevision", "dataRevision"),
        required=("tableId", "schemaRevision", "dataRevision"),
        field_types={
            "tableId": (str,),
            "schemaRevision": (str,),
            "dataRevision": (str,),
        },
    ),
    "relation.searchTargets": _closed_params(
        "RelationSearchTargetsParams",
        allowed=("relationId", "query", "collection", "offset", "limit"),
        required=("relationId",),
        field_types={
            "relationId": (str,),
            "query": (str,),
            "collection": (str, type(None)),
            "offset": (int,),
            "limit": (int,),
        },
    ),
    "relation.createTarget": _closed_params(
        "RelationCreateTargetParams",
        allowed=("relationId", "collection", "label", "idempotencyKey"),
        required=("relationId", "label", "idempotencyKey"),
        field_types={
            "relationId": (str,),
            "collection": (str, type(None)),
            "label": (str,),
            "idempotencyKey": (str,),
        },
    ),
    "relation.updateSingle": _closed_params(
        "RelationUpdateSingleParams",
        allowed=(
            "relationId",
            "sourceItemId",
            "target",
            "expectedSchemaRevision",
            "expectedDateUpdated",
            "idempotencyKey",
        ),
        required=(
            "relationId",
            "sourceItemId",
            "target",
            "expectedSchemaRevision",
            "idempotencyKey",
        ),
        field_types={
            "relationId": (str,),
            "sourceItemId": (str,),
            "target": (dict, type(None)),
            "expectedSchemaRevision": (str,),
            "expectedDateUpdated": (str, type(None)),
            "idempotencyKey": (str,),
        },
    ),
    "relation.previewDelta": _closed_params(
        "RelationPreviewDeltaParams",
        allowed=_RELATION_DELTA_FIELDS,
        required=tuple(name for name in _RELATION_DELTA_FIELDS if name != "expectedDateUpdated"),
    ),
    "relation.applyDelta": _closed_params(
        "RelationApplyDeltaParams",
        allowed=_RELATION_DELTA_FIELDS,
        required=tuple(name for name in _RELATION_DELTA_FIELDS if name != "expectedDateUpdated"),
    ),
    "lookup.list": _closed_params(
        "LookupListParams",
        allowed=("collection",),
        required=("collection",),
        field_types={"collection": (str,)},
    ),
    "lookup.validate": _closed_params(
        "LookupValidateParams",
        allowed=("definition", "existing"),
        required=("definition", "existing"),
        field_types={"definition": (dict,), "existing": (list,)},
    ),
    "lookup.preview": _closed_params(
        "LookupPreviewParams",
        allowed=(
            "contract",
            "collection",
            "fieldRefs",
            "query",
            "requestGeneration",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
            "definitions",
        ),
        required=(
            "contract",
            "collection",
            "fieldRefs",
            "query",
            "requestGeneration",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
            "definitions",
        ),
        field_types={
            "contract": (str,),
            "collection": (str,),
            "fieldRefs": (list,),
            "query": (dict,),
            "requestGeneration": (int,),
            "schemaRevision": (str,),
            "permissionRevision": (str,),
            "lookupRevision": (str,),
            "definitions": (list,),
        },
    ),
    "lookup.query": _closed_params(
        "LookupQueryParams",
        allowed=(
            "contract",
            "collection",
            "fieldRefs",
            "query",
            "requestGeneration",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
        ),
        required=(
            "contract",
            "collection",
            "fieldRefs",
            "query",
            "requestGeneration",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
        ),
        field_types={
            "contract": (str,),
            "collection": (str,),
            "fieldRefs": (list,),
            "query": (dict,),
            "requestGeneration": (int,),
            "schemaRevision": (str,),
            "permissionRevision": (str,),
            "lookupRevision": (str,),
        },
    ),
    "lookup.valuePage": _closed_params(
        "LookupValuePageParams",
        allowed=(
            "collection",
            "fieldRef",
            "sourceRecordId",
            "offset",
            "limit",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
        ),
        required=(
            "collection",
            "fieldRef",
            "sourceRecordId",
            "offset",
            "limit",
            "schemaRevision",
            "permissionRevision",
            "lookupRevision",
        ),
        field_types={
            "collection": (str,),
            "fieldRef": (str,),
            "sourceRecordId": (str,),
            "offset": (int,),
            "limit": (int,),
            "schemaRevision": (str,),
            "permissionRevision": (str,),
            "lookupRevision": (str,),
        },
    ),
    "history.read": _closed_params(
        "HistoryReadParams",
        allowed=(
            "collection",
            "itemId",
            "limit",
            "offset",
            "scope",
            "field",
            "search",
            "dateFrom",
            "dateTo",
            "actorId",
            "actions",
            "recordId",
        ),
        required=("collection", "limit", "offset", "scope", "actions"),
    ),
    "history.previewRestore": _closed_params(
        "HistoryPreviewRestoreParams",
        allowed=("collection", "itemId", "targetRevision", "scope", "field"),
        required=("collection", "itemId", "targetRevision", "scope"),
    ),
    "history.applyRestore": _closed_params(
        "HistoryApplyRestoreParams",
        allowed=("collection", "itemId", "token"),
        required=("collection", "itemId", "token"),
    ),
}


class PocketBaseProductDataService:
    """Maps fixed product use cases to the authenticated loopback sidecar."""

    def __init__(
        self,
        *,
        client: PocketBaseClient,
        transport: PocketBaseTransport,
        session_secret: str,
    ) -> None:
        if not session_secret:
            raise ValueError("PocketBase session secret is required")
        self._client = client
        self._transport = transport
        self._headers = {"X-VibeTable-Session": session_secret}

    async def validate_schema(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/schema/validate", params.root)

    async def apply_schema(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/schema/apply", params.root)

    async def delete_schema(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/schema/delete", params.root)

    async def describe_field_settings(self, params: ProductParams) -> dict[str, Any]:
        table_id = _path_segment(_text(params.root, "tableId"))
        query: dict[str, str] = {}
        if "fieldId" in params.root:
            query["fieldId"] = _text(params.root, "fieldId")
        return _result_object(
            await self._transport.request(
                "GET",
                f"/api/vibetable/v2/field-settings/{table_id}",
                query=query,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def plan_field_change(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v2/field-change/plan", params.root)

    async def apply_field_change(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v2/field-change/apply", params.root)

    async def field_change_status(self, params: ProductParams) -> dict[str, Any]:
        job_id = _path_segment(_text(params.root, "jobId"))
        return _result_object(
            await self._transport.request(
                "GET",
                f"/api/vibetable/v2/field-change/status/{job_id}",
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def cancel_field_change(self, params: ProductParams) -> dict[str, Any]:
        job_id = _path_segment(_text(params.root, "jobId"))
        return await self._post(
            f"/api/vibetable/v2/field-change/cancel/{job_id}",
            {},
        )

    async def list_recycled_fields(self, params: ProductParams) -> dict[str, Any]:
        table_id = _path_segment(_text(params.root, "tableId"))
        return _result_object(
            await self._transport.request(
                "GET",
                f"/api/vibetable/v2/field-recycle-bin/{table_id}",
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def list_tables(self, params: ProductParams) -> dict[str, Any]:
        if params.root:
            raise ValueError("schema.list does not accept parameters")
        return _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/schema/tables",
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def query_page(self, params: ProductParams) -> dict[str, Any]:
        table_id = _text(params.root, "tableId")
        query = _object(params.root, "query")
        page = await self._client.query_page(table_id=table_id, query=query)
        return {
            "rows": page.rows,
            "offset": page.offset,
            "limit": page.limit,
            "filteredRows": page.filtered_rows,
            "totalRows": page.total_rows,
            "snapshot": page.snapshot,
        }

    async def query_view(self, params: ProductParams) -> dict[str, Any]:
        table_id = _text(params.root, "tableId")
        view = _object(params.root, "view")
        result = await self._client.execute_view(table_id=table_id, view=view)
        page = result.page
        return {
            "page": {
                "rows": page.rows,
                "offset": page.offset,
                "limit": page.limit,
                "filteredRows": page.filtered_rows,
                "totalRows": page.total_rows,
                "snapshot": page.snapshot,
            },
            "groupRows": result.group_rows,
            "groupOffset": result.group_offset,
            "groupLimit": result.group_limit,
            "hasMoreGroups": result.has_more_groups,
        }

    async def read_rows(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        row_ids = _array(raw, "rowIds")
        if not all(isinstance(item, str) and item for item in row_ids):
            raise ValueError("rowIds must contain non-empty strings")
        return {
            "rows": await self._client.read_rows(
                table_id=_text(raw, "tableId"),
                row_ids=list(row_ids),
            )
        }

    async def validate_snapshot(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        body: dict[str, Any] = {"snapshot": _object(raw, "snapshot")}
        if "currentQuery" in raw:
            body["currentQuery"] = _object(raw, "currentQuery")
        return await self._post(
            "/api/vibetable/v1/query/validate-snapshot",
            body,
        )

    async def preview_mutation(self, params: ProductParams) -> dict[str, Any]:
        return await self._client.preview_mutation(params.root)

    async def apply_mutation(self, params: ProductParams) -> dict[str, Any]:
        return await self._client.apply_mutation(params.root)

    async def validate_formula(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/formulas/validate", params.root)

    async def validate_formula_draft(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/formulas/draft/validate", params.root)

    async def preview_formula(self, params: ProductParams) -> dict[str, Any]:
        return await self._post("/api/vibetable/v1/formulas/preview", params.root)

    async def create_file_token(self, params: ProductParams) -> dict[str, Any]:
        query = {
            name: _text(params.root, name)
            for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = params.root.get("variant")
        if variant is not None:
            if not isinstance(variant, str):
                raise ValueError("variant must be a string")
            query["variant"] = variant
        return _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def list_attachment_refs(self, params: ProductParams) -> dict[str, Any]:
        query = {name: _text(params.root, name) for name in ("tableId", "recordId", "fieldId")}
        return _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/attachments/refs",
                query=query,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def apply_host_attachment_change(
        self,
        params: ProductParams,
    ) -> dict[str, Any]:
        raw = params.root
        host_paths = _array(raw, "hostPaths")
        remove_names = _array(raw, "removeStoredNames")
        if (
            len(host_paths) > 32
            or len(remove_names) > 32
            or not all(isinstance(item, str) and item for item in host_paths)
            or not all(isinstance(item, str) and item for item in remove_names)
            or (not host_paths and not remove_names)
        ):
            raise ValueError("managed attachment change is invalid")
        expected_digest = _text(raw, "expectedDigest")
        if not _ROW_DIGEST.fullmatch(expected_digest):
            raise ValueError("expectedDigest is invalid")
        request_id = str(uuid.uuid4())
        upload_handles = [f"upload_{index}" for index in range(len(host_paths))]
        request = {
            "contractVersion": "1.0",
            "requestId": request_id,
            "idempotencyKey": f"attachment:{request_id}",
            "tableId": _text(raw, "tableId"),
            "schemaRevision": _text(raw, "schemaRevision"),
            "operations": [
                {
                    "kind": "setAttachments",
                    "recordId": _text(raw, "recordId"),
                    "fieldId": _text(raw, "fieldId"),
                    "uploadHandles": upload_handles,
                    "removeStoredNames": list(remove_names),
                }
            ],
            "actor": {
                "type": "user",
                "id": "local-user",
                "displayName": None,
            },
            "expectedRevision": None,
            "expectedDigest": expected_digest,
        }
        if host_paths:
            result = await self._transport.request_multipart(
                "/api/vibetable/v1/mutations/apply",
                json_body=request,
                uploads=list(zip(upload_handles, host_paths, strict=True)),
                headers=dict(self._headers),
                expected_status=(200,),
            )
        else:
            result = await self._client.apply_mutation(request)
        return _result_object(result)

    async def save_attachment_to_host(
        self,
        params: ProductParams,
    ) -> dict[str, Any]:
        raw = params.root
        query = {
            name: _text(raw, name) for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = raw.get("variant")
        if variant is not None:
            if not isinstance(variant, str) or not variant:
                raise ValueError("variant must be a non-empty string")
            query["variant"] = variant
        token = _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )
        capability = _text(token, "downloadCapability")
        saved_bytes = await self._transport.download_to_file(
            "/api/vibetable/v1/attachments/download",
            query={"capability": capability},
            target_path=_text(raw, "outputPath"),
            headers=dict(self._headers),
            expected_status=(200,),
        )
        return {
            "contractVersion": "1.0",
            "saved": True,
            "bytes": saved_bytes,
        }

    async def reconcile(self, params: ProductParams) -> dict[str, Any]:
        return await self._client.reconcile_realtime(
            table_id=_text(params.root, "tableId"),
            schema_revision=_text(params.root, "schemaRevision"),
            data_revision=_text(params.root, "dataRevision"),
        )

    async def get_table_schema(self, params: ProductParams) -> dict[str, Any]:
        table_id = _text(params.root, "tableId")
        return _result_object(
            await self._transport.request(
                "GET",
                f"/api/vibetable/v1/schema/tables/{_path_segment(table_id)}",
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def describe_schema(self, params: ProductParams) -> dict[str, Any]:
        """Return the frozen renderer relation-capability envelope.

        The trusted desktop table gateway uses ``schema.getTable`` for the raw
        normalized product definition.  Keeping that route separate avoids a
        shape collision with this long-lived renderer contract.
        """
        raw = params.root
        table_id = _path_segment(_text(raw, "collection"))
        request_generation = _integer(raw, "requestGeneration")
        accepts = _array(raw, "accepts")
        if accepts != [
            "vibetable.relation-capabilities.v1",
            "vibetable.lookup-query.v1",
        ]:
            raise ValueError("schema.describe accepts is invalid")
        definition = await self._client.describe_table(table_id)
        catalog = await self._client.describe_relations(table_id)
        relations = catalog.get("relations")
        lookups = catalog.get("lookups")
        if not isinstance(relations, list) or not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid relation catalog")
        schema_revision = _text(definition, "schemaRevision")
        lookup_revision = _lookup_revision(schema_revision, lookups)
        return {
            "contract": "vibetable.schema-describe.v1",
            "collection": table_id,
            "requestGeneration": request_generation,
            "schema": {
                "collection": table_id,
                "primaryKey": "id",
                "primaryDisplayFieldId": _primary_display_field_id(definition),
                "columns": _renderer_columns(definition),
                "normalizedRelations": [
                    _renderer_relation(item, definition)
                    for item in relations
                    if isinstance(item, dict)
                ],
                "schemaRevision": schema_revision,
                "permissionRevision": schema_revision,
                "capabilityHash": _stable_hash(definition),
                "lookupRevision": lookup_revision,
            },
            "capabilities": {
                "contract": "vibetable.relation-capabilities.v1",
                "relationReadV1": True,
                "relationEditV1": True,
                "lookupQueryV1": True,
                "reason": None,
            },
        }

    async def list_table_ids(self) -> list[str]:
        result = _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/schema/tables",
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )
        tables = result.get("tables")
        if not isinstance(tables, list):
            raise ValueError("PocketBase returned an invalid table catalog")
        table_ids: list[str] = []
        for table in tables:
            if not isinstance(table, dict):
                raise ValueError("PocketBase returned an invalid table catalog")
            table_ids.append(_text(table, "tableId"))
        return table_ids

    async def record_exists(self, table_id: str, record_id: str) -> bool:
        if not table_id or not record_id:
            return False
        rows = await self._client.read_rows(
            table_id=table_id,
            row_ids=[record_id],
        )
        return len(rows) == 1

    async def search_relation_targets(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        target_table = raw.get("collection")
        if target_table is not None and (not isinstance(target_table, str) or not target_table):
            raise ValueError("collection must be a non-empty string")
        body: dict[str, Any] = {
            "relationId": _text(raw, "relationId"),
            "query": _optional_text(raw, "query"),
            "offset": _integer(raw, "offset", 0),
            "limit": _integer(raw, "limit", 50),
        }
        if target_table is not None:
            body["targetTableId"] = target_table
        result = await self._post(
            "/api/vibetable/v1/relations/search-targets",
            body,
        )
        # Keep the established renderer relation vocabulary at this boundary.
        items = result.get("items")
        if not isinstance(items, list):
            raise ValueError("PocketBase returned invalid relation targets")
        return {
            "items": [
                {
                    "collection": _text(item, "tableId"),
                    "itemId": _text(item, "recordId"),
                    "label": _text(item, "label"),
                    "junctionId": None,
                    "junctionRevision": None,
                    "junctionValues": {},
                }
                for item in items
                if isinstance(item, dict)
            ],
            "total": _integer(result, "total"),
        }

    async def create_relation_target(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        target_table = raw.get("collection")
        if target_table is not None and (not isinstance(target_table, str) or not target_table):
            raise ValueError("collection must be a non-empty string")
        request_id = _text(raw, "idempotencyKey")
        body: dict[str, Any] = {
            "relationId": _text(raw, "relationId"),
            "label": _text(raw, "label"),
            "requestId": request_id,
            "idempotencyKey": request_id,
            "actor": {"type": "user", "id": "local-user", "displayName": None},
        }
        if target_table is not None:
            body["targetTableId"] = target_table
        result = await self._post(
            "/api/vibetable/v1/relations/create-target",
            body,
        )
        target = result.get("target")
        if not isinstance(target, dict):
            raise ValueError("PocketBase returned an invalid created relation target")
        return {
            "outcome": "committed",
            "target": _renderer_target(target),
            "requestId": request_id,
        }

    async def preview_relation_delta(self, params: ProductParams) -> dict[str, Any]:
        body = _translate_delta(params.root)
        result = await self._post(
            "/api/vibetable/v1/relations/preview-delta",
            body,
        )
        current = result.get("current")
        if not isinstance(current, list):
            raise ValueError("PocketBase returned invalid relation preview")
        return {
            "delta": params.root,
            "current": [_renderer_target(item) for item in current if isinstance(item, dict)],
            "diagnostics": [],
            "canApply": result.get("canApply") is True,
        }

    async def apply_relation_delta(self, params: ProductParams) -> dict[str, Any]:
        body = _translate_delta(params.root)
        result = await self._post(
            "/api/vibetable/v1/relations/apply-delta",
            body,
        )
        current = result.get("current")
        if not isinstance(current, list):
            raise ValueError("PocketBase returned invalid relation result")
        return {
            "outcome": "committed",
            "current": [_renderer_target(item) for item in current if isinstance(item, dict)],
            "schemaRevision": _text(params.root, "expectedSchemaRevision"),
            "requestId": _text(params.root, "idempotencyKey"),
        }

    async def update_single_relation(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        relation_id = _text(raw, "relationId")
        source_record_id = _text_any(raw, "sourceRecordId", "sourceItemId")
        source_hint = relation_id.split(".", 1)[0]
        catalog = await self._client.describe_relations(source_hint)
        relations = catalog.get("relations")
        if not isinstance(relations, list):
            raise ValueError("PocketBase returned an invalid relation catalog")
        descriptor = next(
            (
                item
                for item in relations
                if isinstance(item, dict) and item.get("relationId") == relation_id
            ),
            None,
        )
        if not isinstance(descriptor, dict) or descriptor.get("cardinality") != "one":
            raise ValueError("single relation is unavailable")
        source_table = _text(descriptor, "sourceTableId")
        physical_name = _text(descriptor, "physicalName")
        target_table = _text(descriptor, "targetTableId")
        rows = await self._client.read_rows(
            table_id=source_table,
            row_ids=[source_record_id],
        )
        if len(rows) != 1:
            raise ValueError("relation source record was not found")
        current_ids = _relation_ids(rows[0].get(physical_name))
        target = raw.get("target")
        target_ref = None if target is None else _translate_target(target, "target")
        if target_ref is not None and target_ref["tableId"] != target_table:
            raise ValueError("relation target belongs to another table")
        desired_id = target_ref["recordId"] if target_ref is not None else None
        adds = [] if desired_id is None or desired_id in current_ids else [target_ref]
        removes = [
            {
                "tableId": target_table,
                "recordId": record_id,
                "label": record_id,
            }
            for record_id in current_ids
            if record_id != desired_id
        ]
        request_id = _text_any(raw, "requestId", "idempotencyKey")
        result = await self._post(
            "/api/vibetable/v1/relations/apply-delta",
            {
                "relationId": relation_id,
                "sourceRecordId": source_record_id,
                "schemaRevision": _text_any(
                    raw,
                    "schemaRevision",
                    "expectedSchemaRevision",
                ),
                "adds": adds,
                "removes": removes,
                "requestId": request_id,
                "idempotencyKey": _text(raw, "idempotencyKey"),
                "expectedDigest": raw.get("expectedDigest"),
                "actor": raw.get(
                    "actor",
                    {"type": "user", "id": "local-user", "displayName": None},
                ),
            },
        )
        return {
            "outcome": "committed",
            "current": target,
            "schemaRevision": _text_any(
                raw,
                "schemaRevision",
                "expectedSchemaRevision",
            ),
            "requestId": request_id,
            "receipt": result.get("receipt"),
        }

    async def list_lookups(self, params: ProductParams) -> dict[str, Any]:
        table_id = _text(params.root, "collection")
        result = _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/lookups/describe",
                query={"tableId": table_id},
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )
        lookups = result.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        schema_revision = _text(result, "schemaRevision")
        return {
            "collection": table_id,
            "definitions": [_renderer_lookup(item) for item in lookups if isinstance(item, dict)],
            "lookupRevision": _lookup_revision(schema_revision, lookups),
        }

    async def validate_lookup(self, params: ProductParams) -> dict[str, Any]:
        renderer = _object(params.root, "definition")
        table_id = _text(renderer, "collection")
        schema = await self._client.describe_table(table_id)
        field = await self._normalized_lookup_field(renderer, schema)
        proposed = _replace_lookup_field(schema, field, allow_create=True)
        await self._post(
            "/api/vibetable/v1/schema/validate",
            {
                "definition": proposed,
                "expectedRevision": _schema_revision_number(_text(schema, "schemaRevision")),
            },
        )
        result = _clone_json(renderer)
        result["state"] = "valid"
        result["diagnostics"] = []
        return {
            "definition": result,
            "valid": True,
            "diagnostics": [],
            "lookupRevision": _stable_hash(
                {
                    "schemaRevision": schema["schemaRevision"],
                    "definition": result,
                }
            ),
        }

    async def preview_lookup(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        table_id = _text(raw, "collection")
        schema = await self._client.describe_table(table_id)
        if _text(raw, "schemaRevision") != _text(schema, "schemaRevision"):
            raise ValueError("Lookup schema revision does not match")
        definitions = _array(raw, "definitions")
        proposed = _clone_json(schema)
        fields = proposed.get("fields")
        if not isinstance(fields, list):
            raise ValueError("PocketBase returned an invalid field catalog")
        fields = [
            item for item in fields if not (isinstance(item, dict) and item.get("kind") == "lookup")
        ]
        field_ids: list[str] = []
        rendered_by_field: dict[str, dict[str, Any]] = {}
        for item in definitions:
            if not isinstance(item, dict):
                raise ValueError("definitions must contain objects")
            field = await self._normalized_lookup_field(item, schema)
            fields.append(field)
            field_ids.append(_text(field, "fieldId"))
            rendered_by_field[_text(field, "physicalName")] = item
        proposed["fields"] = fields
        query = dict(_object(raw, "query"))
        groups = query.pop("groups", [])
        if not isinstance(groups, list):
            raise ValueError("query.groups must be an array")
        page = await self._post(
            "/api/vibetable/v1/lookups/preview",
            {
                "definition": proposed,
                "fieldIds": field_ids,
                "query": query,
            },
        )
        return _lookup_query_envelope(
            raw=raw,
            page=page,
            definitions=rendered_by_field,
            groups=_group_lookup_rows(page.get("rows", []), groups),
        )

    async def _normalized_lookup_field(
        self,
        renderer: dict[str, Any],
        source_schema: dict[str, Any],
    ) -> dict[str, Any]:
        table_id = _text(renderer, "collection")
        if table_id != _text(source_schema, "tableId"):
            raise ValueError("Lookup collection does not match its schema")
        lookup_id = _text(renderer, "lookupId")
        field_id = _lookup_field_id(table_id, lookup_id)
        path = _array(renderer, "path")
        if not path:
            raise ValueError("Lookup paths must contain at least one relation")
        current_schema = source_schema
        normalized_path: list[dict[str, str]] = []
        relation_field_id = ""
        relation: dict[str, Any] = {}
        mode = ""
        terminal_polymorphic = False
        for index, step in enumerate(path):
            if not isinstance(step, dict):
                raise ValueError("Lookup path must contain objects")
            current_table_id = _text(current_schema, "tableId")
            relation_id = _text(step, "relationId")
            relation_field_id = _lookup_field_id(current_table_id, relation_id)
            relation_field = _schema_field(current_schema, relation_field_id)
            raw_relation = relation_field.get("relation")
            if not isinstance(raw_relation, dict):
                raise ValueError(f"Lookup path step {index} does not reference a relation field")
            relation = raw_relation
            mode = relation.get("mode") or (
                "junction" if relation.get("junctionTableId") else "direct"
            )
            m2a_collection = step.get("m2aCollection")
            if m2a_collection is not None and (
                not isinstance(m2a_collection, str) or not m2a_collection
            ):
                raise ValueError("m2aCollection must be a non-empty string")
            target_table_id = _text(relation, "targetTableId")
            if mode == "m2a":
                allowed = relation.get("allowedTargetTableIds")
                if not isinstance(allowed, list) or not all(
                    isinstance(item, str) and item for item in allowed
                ):
                    raise ValueError("m2a relation allowlist is invalid")
                if m2a_collection is None and index < len(path) - 1:
                    raise ValueError("intermediate m2a Lookup steps must select m2aCollection")
                if m2a_collection is not None:
                    if m2a_collection not in allowed:
                        raise ValueError("m2aCollection is not allowed by the relation")
                    target_table_id = m2a_collection
                else:
                    terminal_polymorphic = True
            elif m2a_collection is not None:
                raise ValueError("m2aCollection is only valid for m2a relations")
            normalized_step = {"relationFieldId": relation_field_id}
            if m2a_collection is not None:
                normalized_step["m2aCollection"] = m2a_collection
            normalized_path.append(normalized_step)
            if not terminal_polymorphic:
                current_schema = await self._client.describe_table(target_table_id)
        source = _object(renderer, "source")
        source_kind = _text(source, "kind")
        target_field_id = ""
        junction_field_id = ""
        target_field_ids: dict[str, str] = {}
        if source_kind == "junction_field":
            junction_table_id = relation.get("junctionTableId")
            if not isinstance(junction_table_id, str) or not junction_table_id:
                raise ValueError("junction Lookup requires a junction relation")
            junction_schema = await self._client.describe_table(junction_table_id)
            junction_field_id = _schema_field_id(junction_schema, _text(source, "fieldRef"))
            target_field_id = junction_field_id
        elif mode == "m2a" and terminal_polymorphic:
            mappings = _array(renderer, "m2aFieldMapping")
            for mapping in mappings:
                if not isinstance(mapping, dict):
                    raise ValueError("m2aFieldMapping must contain objects")
                target_table_id = _text(mapping, "collection")
                target_schema = await self._client.describe_table(target_table_id)
                target_field_ids[target_table_id] = _schema_field_id(
                    target_schema, _text(mapping, "fieldRef")
                )
            allowed = relation.get("allowedTargetTableIds")
            if not isinstance(allowed, list) or set(target_field_ids) != set(allowed):
                raise ValueError("m2a Lookup must map every allowed target table")
            default_target = _text(relation, "targetTableId")
            target_field_id = target_field_ids[default_target]
        elif source_kind in {"target_field", "lookup"}:
            reference = (
                _text(source, "lookupId") if source_kind == "lookup" else _text(source, "fieldRef")
            )
            target_field_id = _schema_field_id(current_schema, reference)
        else:
            raise ValueError("Lookup source kind is unsupported")
        aggregation = {
            "single": "first",
            "values": "none",
            "distinct_values": "distinct",
            "related_count": "count",
            "non_null_count": "countNonNull",
            "sum": "sum",
            "average": "avg",
            "min": "min",
            "max": "max",
        }.get(_text(renderer, "aggregation"))
        if aggregation is None:
            raise ValueError("Lookup aggregation is unsupported")
        output_type = _text(renderer, "outputType")
        _data_type, storage_type = _lookup_output_storage(output_type)
        if aggregation in {"none", "distinct"}:
            storage_type = "json"
        constraints: list[dict[str, Any]] = []
        if output_type == "decimal":
            scale = renderer.get("outputScale")
            if isinstance(scale, bool) or not isinstance(scale, int) or not 0 <= scale <= 18:
                raise ValueError("decimal Lookup requires outputScale between 0 and 18")
            constraints.append({"kind": "precisionScale", "precision": 38, "scale": scale})
        return {
            "fieldId": field_id,
            "physicalName": _text(renderer, "fieldKey"),
            "displayName": _text(renderer, "displayName"),
            "kind": "lookup",
            "dataType": "lookup",
            "storageType": storage_type,
            "nullable": True,
            "defaultValue": None,
            "constraints": constraints,
            "editor": {"kind": "text", "config": {}},
            "readOnly": True,
            "formula": None,
            "relation": None,
            "lookup": {
                "relationFieldId": normalized_path[0]["relationFieldId"],
                "path": normalized_path,
                "targetFieldId": target_field_id,
                "junctionFieldId": junction_field_id,
                "targetFieldIds": target_field_ids,
                "aggregate": aggregation,
            },
            "attachmentPolicy": None,
        }

    async def query_lookups(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        query = dict(_object(raw, "query"))
        groups = query.pop("groups", [])
        if not isinstance(groups, list):
            raise ValueError("query.groups must be an array")
        table_id = _text(raw, "collection")
        catalog = await self._client.describe_relations(table_id)
        lookups = catalog.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        current_schema_revision = _text(catalog, "schemaRevision")
        current_lookup_revision = _lookup_revision(current_schema_revision, lookups)
        if (
            _text(raw, "schemaRevision") != current_schema_revision
            or _text(raw, "permissionRevision") != current_schema_revision
            or _text(raw, "lookupRevision") != current_lookup_revision
        ):
            raise ValueError("Lookup query revisions are stale")
        page = await self._client.query_lookups(
            table_id=table_id,
            schema_revision=current_schema_revision,
            query=query,
        )
        definitions = {
            item.get("physicalName"): _renderer_lookup(item)
            for item in lookups
            if isinstance(item, dict) and isinstance(item.get("physicalName"), str)
        }
        field_refs = _array(raw, "fieldRefs")
        columns = []
        for field_ref in field_refs:
            if not isinstance(field_ref, str) or field_ref not in definitions:
                raise ValueError("fieldRefs contains an unknown Lookup")
            definition = definitions[field_ref]
            columns.append(
                {
                    "fieldRef": field_ref,
                    "title": definition["displayName"],
                    "outputType": definition["outputType"],
                    "nullable": True,
                    "scale": definition["outputScale"],
                    "state": definition["state"],
                }
            )
        grouped_rows: list[dict[str, Any]] = []
        if groups:
            all_rows: list[dict[str, Any]] = []
            for offset in range(0, page.filtered_rows, 500):
                group_page = await self._client.query_lookups(
                    table_id=table_id,
                    schema_revision=current_schema_revision,
                    query={**query, "offset": offset, "limit": 500},
                )
                all_rows.extend(group_page.rows)
            grouped_rows = _group_lookup_rows(all_rows, groups)
        return {
            "contract": "vibetable.lookup-query.v1",
            "collection": table_id,
            "requestGeneration": _integer(raw, "requestGeneration"),
            "schemaRevision": current_schema_revision,
            "permissionRevision": current_schema_revision,
            "lookupRevision": current_lookup_revision,
            "columns": columns,
            "rows": page.rows,
            "groups": grouped_rows,
            "offset": page.offset,
            "limit": page.limit,
            "filteredRows": page.filtered_rows,
            "totalRows": page.total_rows,
            "snapshot": page.snapshot,
        }

    async def lookup_value_page(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        table_id = _text(raw, "collection")
        catalog = await self._client.describe_relations(table_id)
        lookups = catalog.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        schema_revision = _text(catalog, "schemaRevision")
        if (
            _text(raw, "schemaRevision") != schema_revision
            or _text(raw, "permissionRevision") != schema_revision
            or _text(raw, "lookupRevision") != _lookup_revision(schema_revision, lookups)
        ):
            raise ValueError("Lookup value page revisions are stale")
        field_ref = _text(raw, "fieldRef")
        lookup = next(
            (
                item
                for item in lookups
                if isinstance(item, dict) and item.get("physicalName") == field_ref
            ),
            None,
        )
        if not isinstance(lookup, dict):
            raise ValueError("fieldRef does not identify a Lookup")
        offset = _integer(raw, "offset")
        limit = _integer(raw, "limit")
        if offset < 0 or limit < 1 or limit > 500:
            raise ValueError("Lookup value page paging is invalid")
        return await self._client.lookup_value_page(
            table_id=table_id,
            schema_revision=schema_revision,
            source_record_id=_text(raw, "sourceRecordId"),
            field_id=_text(lookup, "fieldId"),
            offset=offset,
            limit=limit,
        )

    async def read_history(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        query: dict[str, Any] = {
            "collection": _text_any(raw, "collection", "tableId"),
            "limit": _integer(raw, "limit", 50),
            "offset": _integer(raw, "offset", 0),
            "scope": _optional_text(raw, "scope") or "row",
        }
        for source, target in (
            ("itemId", "itemId"),
            ("field", "field"),
            ("search", "search"),
            ("actorId", "actorId"),
            ("dateFrom", "dateFrom"),
            ("dateTo", "dateTo"),
            ("recordId", "recordId"),
        ):
            value = raw.get(source)
            if value is not None:
                if not isinstance(value, str) or not value:
                    raise ValueError(f"{source} must be a non-empty string")
                query[target] = value
        actions = raw.get("actions", [])
        if not isinstance(actions, list) or not all(
            isinstance(item, str) and item for item in actions
        ):
            raise ValueError("actions must contain non-empty strings")
        if actions:
            query["action"] = actions
        return _result_object(
            await self._transport.request(
                "GET",
                "/api/vibetable/v1/history/change-sets",
                query=query,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )

    async def preview_history_restore(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        body: dict[str, Any] = {
            "collection": _text_any(raw, "collection", "tableId"),
            "itemId": _text_any(raw, "itemId", "recordId"),
            "targetRevision": _text(raw, "targetRevision"),
            "scope": _optional_text(raw, "scope") or "row",
        }
        field = raw.get("field")
        if field is not None:
            if not isinstance(field, str) or not field:
                raise ValueError("field must be a non-empty string")
            body["field"] = field
        return await self._post(
            "/api/vibetable/v1/history/restore-preview",
            body,
        )

    async def apply_history_restore(self, params: ProductParams) -> dict[str, Any]:
        raw = params.root
        return await self._post(
            "/api/vibetable/v1/history/restore-apply",
            {
                "collection": _text_any(raw, "collection", "tableId"),
                "itemId": _text_any(raw, "itemId", "recordId"),
                "token": _text(raw, "token"),
            },
        )

    async def _post(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        return _result_object(
            await self._transport.request(
                "POST",
                path,
                json_body=body,
                headers=dict(self._headers),
                expected_status=(200,),
            )
        )


def _translate_delta(raw: dict[str, Any]) -> dict[str, Any]:
    updates = raw.get("updates", [])
    if updates:
        raise ValueError("junction context updates require a junction relation")
    return {
        "relationId": _text(raw, "relationId"),
        "sourceRecordId": _text_any(raw, "sourceRecordId", "sourceItemId"),
        "schemaRevision": _text_any(raw, "schemaRevision", "expectedSchemaRevision"),
        "adds": [_translate_target(item, "add") for item in _array(raw, "adds")],
        "removes": [_translate_target(item, "remove") for item in _array(raw, "removes")],
        "requestId": _text_any(raw, "requestId", "idempotencyKey"),
        "idempotencyKey": _text(raw, "idempotencyKey"),
        "expectedDigest": raw.get("expectedDigest"),
        "actor": raw.get(
            "actor",
            {"type": "user", "id": "local-user", "displayName": None},
        ),
    }


def _renderer_target(value: dict[str, Any]) -> dict[str, Any]:
    junction_values = value.get("junctionValues", {})
    if not isinstance(junction_values, dict):
        raise ValueError("PocketBase returned invalid junction values")
    return {
        "collection": _text(value, "tableId"),
        "itemId": _text(value, "recordId"),
        "label": _text(value, "label"),
        "junctionId": value.get("junctionId") or None,
        "junctionRevision": value.get("junctionRevision") or None,
        "junctionValues": junction_values,
    }


def _renderer_relation(
    value: dict[str, Any],
    definition: dict[str, Any],
) -> dict[str, Any]:
    mode = _text(value, "mode")
    cardinality = _text(value, "cardinality")
    if mode == "m2a":
        kind = "m2a"
    elif mode == "junction":
        kind = "m2m"
    elif cardinality == "one":
        kind = "m2o"
    else:
        kind = "m2m"
    source_field_id = _text(value, "sourceFieldId")
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    source_field = next(
        (
            field
            for field in fields
            if isinstance(field, dict) and field.get("fieldId") == source_field_id
        ),
        {},
    )
    target_table = _text(value, "targetTableId")
    allowed = value.get("allowedTargetTableIds")
    # Go encodes an uninitialized []string as null. Treat it as the empty
    # allowlist used by ordinary non-polymorphic relations.
    if allowed is None:
        allowed = []
    if not isinstance(allowed, list) or not all(isinstance(item, str) and item for item in allowed):
        raise ValueError("PocketBase returned invalid relation allowlist")
    if not allowed and kind != "m2a":
        allowed = [target_table]
    junction_table = value.get("junctionTableId")
    junction: dict[str, Any] | None = None
    if isinstance(junction_table, str) and junction_table:
        junction = {
            "collection": junction_table,
            "sourceField": value.get("junctionSourceFieldId") or "",
            "targetField": value.get("junctionTargetFieldId") or "",
            "collectionField": (value.get("junctionDiscriminatorFieldId") or None),
            "sortField": None,
            "contextFields": [],
        }
    delete_policy = value.get("deletePolicy")
    on_delete = {
        "set-null": "nullify",
        "setNull": "nullify",
        "nullify": "nullify",
        "restrict": "restrict",
        "cascade": "cascade",
    }.get(str(delete_policy), "restrict")
    return {
        "relationId": _text(value, "relationId"),
        "fieldRef": _text(source_field, "physicalName"),
        "sourceCollection": _text(value, "sourceTableId"),
        "kind": kind,
        "relatedCollection": None if kind == "m2a" else target_table,
        "allowedCollections": allowed,
        "manyField": _text(source_field, "physicalName"),
        "oneField": None,
        "junction": junction,
        "unique": cardinality == "one",
        "nullable": source_field.get("nullable") is True,
        "onDelete": on_delete,
        "preset": "standard",
        "selfRelation": target_table == value.get("sourceTableId"),
        "managed": True,
        "pairId": value.get("pairId") if isinstance(value.get("pairId"), str) else "",
        "reciprocalFieldId": (
            value.get("reciprocalFieldId")
            if isinstance(value.get("reciprocalFieldId"), str)
            else ""
        ),
        "state": "valid",
        "displayTemplate": None,
        "diagnostics": [],
    }


def _primary_display_field_id(definition: dict[str, Any]) -> str:
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    configured = definition.get("primaryDisplayFieldId")
    if isinstance(configured, str) and configured:
        for field in fields:
            if isinstance(field, dict) and field.get("fieldId") == configured:
                return configured
    for field in fields:
        if (
            isinstance(field, dict)
            and field.get("readOnly") is not True
            and field.get("kind") != "relation"
        ):
            return _text(field, "fieldId")
    for field in fields:
        if isinstance(field, dict):
            return _text(field, "fieldId")
    return ""


def _renderer_columns(definition: dict[str, Any]) -> list[dict[str, Any]]:
    table_id = _text(definition, "tableId")
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    columns: list[dict[str, Any]] = [
        {
            "name": "id",
            "title": "ID",
            "fieldId": "id",
            "kind": "system",
            "relationId": None,
            "lookupId": None,
            "dataType": "text",
            "editable": False,
            "nullable": False,
            "scale": None,
            "precision": None,
            "attachmentPolicy": None,
        }
    ]
    for field in fields:
        if not isinstance(field, dict):
            raise ValueError("PocketBase returned an invalid field catalog")
        field_id = _text(field, "fieldId")
        kind = _text(field, "kind")
        product_data_type = _text(field, "dataType")
        if product_data_type == "formula":
            formula = field.get("formula")
            if not isinstance(formula, dict):
                raise ValueError("Formula field omitted its formula definition")
            product_data_type = _text(formula, "resultType")
        elif product_data_type == "lookup":
            storage_type = field.get("storageType")
            product_data_type = (
                storage_type if isinstance(storage_type, str) and storage_type else "text"
            )
        data_type = _renderer_data_type(product_data_type)
        precision, scale = _precision_scale(field.get("constraints"))
        columns.append(
            {
                "name": _text(field, "physicalName"),
                "title": _text(field, "displayName"),
                "fieldId": field_id,
                "kind": kind,
                "relationId": f"{table_id}.{field_id}" if kind == "relation" else None,
                "lookupId": f"{table_id}.{field_id}" if kind == "lookup" else None,
                "dataType": data_type,
                "editable": field.get("readOnly") is not True,
                "nullable": field.get("nullable") is True,
                "scale": scale,
                "precision": precision,
                "attachmentPolicy": (
                    field.get("attachmentPolicy") if kind == "attachment" else None
                ),
            }
        )
    return columns


def _schema_revision_number(value: str) -> int:
    match = _SCHEMA_REVISION.fullmatch(value)
    if match is None:
        raise ValueError("schemaRevision is invalid")
    return int(match.group(1))


def _lookup_field_id(collection: str, lookup_id: str) -> str:
    prefix = collection + "."
    if not lookup_id.startswith(prefix):
        raise ValueError("Lookup id must be scoped to its collection")
    field_id = lookup_id[len(prefix) :]
    if not field_id or "." in field_id or _PATH_SEGMENT.fullmatch(field_id) is None:
        raise ValueError("Lookup id contains an invalid field id")
    return field_id


def _schema_field(schema: dict[str, Any], field_id: str) -> dict[str, Any]:
    fields = schema.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    for field in fields:
        if isinstance(field, dict) and field.get("fieldId") == field_id:
            return field
    raise ValueError(f"schema field {field_id!r} was not found")


def _schema_field_id(schema: dict[str, Any], reference: str) -> str:
    suffix = reference.rsplit(".", 1)[-1]
    fields = schema.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    matches = [
        field
        for field in fields
        if isinstance(field, dict)
        and (field.get("fieldId") in {reference, suffix} or field.get("physicalName") == reference)
    ]
    if len(matches) != 1:
        raise ValueError(f"schema field reference {reference!r} is ambiguous or missing")
    return _text(matches[0], "fieldId")


def _replace_lookup_field(
    schema: dict[str, Any],
    field: dict[str, Any],
    *,
    allow_create: bool,
) -> dict[str, Any]:
    result = _clone_json(schema)
    fields = result.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    field_id = _text(field, "fieldId")
    indexes = [
        index
        for index, current in enumerate(fields)
        if isinstance(current, dict) and current.get("fieldId") == field_id
    ]
    if allow_create:
        if indexes:
            raise ValueError("Lookup field already exists")
        fields.append(field)
    else:
        if len(indexes) != 1:
            raise ValueError("Lookup field was not found")
        fields[indexes[0]] = field
    return result


def _lookup_output_storage(output_type: str) -> tuple[str, str]:
    result = {
        "text": ("shortText", "text"),
        "integer": ("integer", "number"),
        "decimal": ("decimal", "number"),
        "boolean": ("boolean", "bool"),
        "date": ("date", "date"),
        "datetime": ("dateTime", "date"),
        "time": ("time", "text"),
        "json": ("json", "json"),
    }.get(output_type)
    if result is None:
        raise ValueError("Lookup output type is unsupported")
    return result


def _group_lookup_rows(
    rows: Any,
    groups: list[Any],
) -> list[dict[str, Any]]:
    if not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows):
        raise ValueError("Lookup rows are invalid")
    normalized: list[tuple[str, str]] = []
    for group in groups:
        if not isinstance(group, dict):
            raise ValueError("Lookup groups must contain objects")
        field = _text(group, "fieldRef")
        direction = group.get("direction", "asc")
        if direction not in {"asc", "desc"}:
            raise ValueError("Lookup group direction is invalid")
        normalized.append((field, direction))
    result: list[dict[str, Any]] = []

    def visit(items: list[dict[str, Any]], depth: int, path: list[Any]) -> None:
        if depth >= len(normalized):
            return
        field, direction = normalized[depth]
        buckets: dict[str, tuple[Any, list[dict[str, Any]]]] = {}
        for row in items:
            value = row.get(field)
            key = json.dumps(
                value,
                ensure_ascii=False,
                allow_nan=False,
                sort_keys=True,
                separators=(",", ":"),
            )
            if key not in buckets:
                buckets[key] = (value, [])
            buckets[key][1].append(row)
        ordered = sorted(
            buckets.values(),
            key=lambda item: json.dumps(
                item[0],
                ensure_ascii=False,
                allow_nan=False,
                sort_keys=True,
                separators=(",", ":"),
            ),
            reverse=direction == "desc",
        )
        for value, bucket_rows in ordered:
            next_path = [*path, value]
            result.append(
                {
                    "path": path,
                    "key": value,
                    "count": len(bucket_rows),
                    "aggregates": {},
                    "childCursor": None,
                }
            )
            visit(bucket_rows, depth + 1, next_path)

    visit(rows, 0, [])
    return result


def _lookup_query_envelope(
    *,
    raw: dict[str, Any],
    page: dict[str, Any],
    definitions: dict[str, dict[str, Any]],
    groups: list[dict[str, Any]],
) -> dict[str, Any]:
    field_refs = _array(raw, "fieldRefs")
    columns: list[dict[str, Any]] = []
    for field_ref in field_refs:
        definition = definitions.get(field_ref) if isinstance(field_ref, str) else None
        if not isinstance(definition, dict):
            raise ValueError("fieldRefs contains an unknown Lookup")
        columns.append(
            {
                "fieldRef": field_ref,
                "title": _text(definition, "displayName"),
                "outputType": _text(definition, "outputType"),
                "nullable": True,
                "scale": definition.get("outputScale"),
                "state": definition.get("state", "valid"),
            }
        )
    rows = page.get("rows")
    if not isinstance(rows, list):
        raise ValueError("Lookup preview returned invalid rows")
    return {
        "contract": "vibetable.lookup-query.v1",
        "collection": _text(raw, "collection"),
        "requestGeneration": _integer(raw, "requestGeneration"),
        "schemaRevision": _text(raw, "schemaRevision"),
        "permissionRevision": _text(raw, "permissionRevision"),
        "lookupRevision": _text(raw, "lookupRevision"),
        "columns": columns,
        "rows": rows,
        "groups": groups,
        "offset": _integer(page, "offset"),
        "limit": _integer(page, "limit"),
        "filteredRows": _integer(page, "filteredRows"),
        "totalRows": _integer(page, "totalRows"),
        "snapshot": page.get("querySnapshot"),
    }


def _renderer_lookup(value: dict[str, Any]) -> dict[str, Any]:
    aggregate = _text(value, "aggregate")
    aggregation = {
        "none": "values",
        "first": "single",
        "count": "related_count",
        "countNonNull": "non_null_count",
        "distinct": "distinct_values",
        "sum": "sum",
        "avg": "average",
        "min": "min",
        "max": "max",
    }.get(aggregate)
    if aggregation is None:
        raise ValueError("PocketBase returned an invalid Lookup aggregate")
    output_type = _renderer_data_type(_text(value, "outputStorage"))
    if output_type not in {
        "text",
        "integer",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "time",
        "json",
    }:
        raise ValueError("PocketBase returned an invalid Lookup output type")
    table_id = _text(value, "tableId")
    relation_field_id = _text(value, "relationFieldId")
    raw_path = value.get("path")
    if raw_path is None:
        renderer_path = [{"relationId": f"{table_id}.{relation_field_id}"}]
    elif not isinstance(raw_path, list) or not raw_path:
        raise ValueError("PocketBase returned an invalid Lookup path")
    else:
        renderer_path = []
        for step in raw_path:
            if not isinstance(step, dict):
                raise ValueError("PocketBase returned an invalid Lookup path")
            relation_id = step.get("relationId")
            if not isinstance(relation_id, str) or not relation_id:
                raise ValueError("PocketBase returned an invalid Lookup path")
            normalized_step = {"relationId": relation_id}
            m2a_collection = step.get("m2aCollection")
            if m2a_collection is not None:
                if not isinstance(m2a_collection, str) or not m2a_collection:
                    raise ValueError("PocketBase returned an invalid Lookup m2a path")
                normalized_step["m2aCollection"] = m2a_collection
            renderer_path.append(normalized_step)
    junction_field_id = value.get("junctionFieldId")
    if junction_field_id is not None and (
        not isinstance(junction_field_id, str) or not junction_field_id
    ):
        raise ValueError("PocketBase returned an invalid junction Lookup field")
    target_field_ids = value.get("targetFieldIds", {})
    if not isinstance(target_field_ids, dict) or not all(
        isinstance(table, str) and table and isinstance(field, str) and field
        for table, field in target_field_ids.items()
    ):
        raise ValueError("PocketBase returned invalid m2a Lookup mappings")
    return {
        "lookupId": _text(value, "lookupId"),
        "collection": table_id,
        "fieldKey": _text(value, "physicalName"),
        "displayName": _text(value, "displayName"),
        "path": renderer_path,
        "source": {
            "kind": "junction_field" if junction_field_id else "target_field",
            "fieldRef": junction_field_id or _text(value, "targetFieldId"),
        },
        "m2aFieldMapping": [
            {"collection": table, "fieldRef": field}
            for table, field in sorted(target_field_ids.items())
        ],
        "aggregation": aggregation,
        "outputType": output_type,
        "outputScale": None,
        "revision": _integer(value, "revision"),
        "state": "valid",
        "diagnostics": [],
        "dependencies": [step["relationId"] for step in renderer_path],
    }


def _renderer_data_type(value: str) -> str:
    result = {
        "text": "text",
        "shortText": "text",
        "longText": "text",
        "richText": "text",
        "editor": "text",
        "email": "text",
        "url": "text",
        "uuid": "text",
        "select": "text",
        "multiSelect": "text",
        "list": "text",
        "hash": "text",
        "secret": "text",
        "integer": "integer",
        "number": "decimal",
        "float": "decimal",
        "decimal": "decimal",
        "boolean": "boolean",
        "bool": "boolean",
        "date": "date",
        "dateTime": "datetime",
        "autoDate": "datetime",
        "autodate": "datetime",
        "time": "time",
        "json": "json",
        "geoPoint": "json",
        "geoJson": "json",
        "relation": "text",
        "file": "text",
        "formula": "text",
        "lookup": "text",
    }.get(value)
    if result is None:
        raise ValueError("PocketBase returned an unknown data type")
    return result


def _precision_scale(value: Any) -> tuple[int | None, int | None]:
    if not isinstance(value, list):
        raise ValueError("PocketBase returned invalid field constraints")
    for constraint in value:
        if isinstance(constraint, dict) and constraint.get("kind") == "precisionScale":
            precision = constraint.get("precision")
            scale = constraint.get("scale")
            if (
                isinstance(precision, bool)
                or not isinstance(precision, int)
                or isinstance(scale, bool)
                or not isinstance(scale, int)
            ):
                raise ValueError("PocketBase returned invalid precision constraints")
            return precision, scale
    return None, None


def _stable_hash(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode()
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def _clone_json(value: Any) -> Any:
    return json.loads(
        json.dumps(
            value,
            ensure_ascii=False,
            allow_nan=False,
            separators=(",", ":"),
            sort_keys=True,
        )
    )


def _lookup_revision(schema_revision: str, lookups: list[Any]) -> str:
    return _stable_hash({"schemaRevision": schema_revision, "lookups": lookups})


def _translate_target(value: Any, label: str) -> dict[str, str]:
    if not isinstance(value, dict):
        raise ValueError(f"{label} target must be an object")
    nested = value.get("target")
    target = nested if isinstance(nested, dict) else value
    return {
        "tableId": _text_any(target, "tableId", "collection"),
        "recordId": _text_any(target, "recordId", "itemId"),
        "label": _optional_text(target, "label") or _text_any(target, "recordId", "itemId"),
    }


def _validate_value(value: Any, depth: int) -> None:
    if depth > _MAX_DEPTH:
        raise ValueError("product params are too deeply nested")
    if isinstance(value, dict):
        for key, item in value.items():
            if not isinstance(key, str) or key in _FORBIDDEN_KEYS:
                raise ValueError("product params contain a forbidden field")
            _validate_value(item, depth + 1)
    elif isinstance(value, list):
        for item in value:
            _validate_value(item, depth + 1)
    elif value is not None and not isinstance(value, (str, int, float, bool)):
        raise ValueError("product params must contain JSON values")


def _result_object(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("PocketBase returned an invalid product response")
    return value


def _object(value: dict[str, Any], name: str) -> dict[str, Any]:
    result = value.get(name)
    if not isinstance(result, dict):
        raise ValueError(f"{name} must be an object")
    return result


def _array(value: dict[str, Any], name: str) -> list[Any]:
    result = value.get(name, [])
    if not isinstance(result, list):
        raise ValueError(f"{name} must be an array")
    return result


def _text(value: dict[str, Any], name: str) -> str:
    result = value.get(name)
    if not isinstance(result, str) or not result:
        raise ValueError(f"{name} must be a non-empty string")
    return result


def _text_any(value: dict[str, Any], *names: str) -> str:
    for name in names:
        result = value.get(name)
        if isinstance(result, str) and result:
            return result
    raise ValueError(f"{'/'.join(names)} must be a non-empty string")


def _optional_text(value: dict[str, Any], name: str) -> str:
    result = value.get(name, "")
    if not isinstance(result, str):
        raise ValueError(f"{name} must be a string")
    return result


def _integer(value: dict[str, Any], name: str, default: int | None = None) -> int:
    result = value.get(name, default)
    if isinstance(result, bool) or not isinstance(result, int):
        raise ValueError(f"{name} must be an integer")
    return result


def _path_segment(value: str) -> str:
    if not _PATH_SEGMENT.fullmatch(value):
        raise ValueError("table id is invalid")
    return value


def _relation_ids(value: Any) -> list[str]:
    if value is None or value == "":
        return []
    if isinstance(value, str):
        return [value]
    if isinstance(value, list) and all(isinstance(item, str) and item for item in value):
        return list(value)
    raise ValueError("PocketBase returned an invalid relation value")


__all__ = [
    "PRODUCT_PARAM_MODELS",
    "PocketBaseProductDataService",
    "ProductParams",
]
