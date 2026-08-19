"""Closed parameter contracts for renderer-reachable product RPC methods."""

from __future__ import annotations

import json
from typing import ClassVar

from pydantic import JsonValue, RootModel, model_validator

from backend.contracts.schema_v2 import ApplyRequestV2

_MAX_PARAMS_BYTES = 1 << 20
_MAX_DEPTH = 32
_FORBIDDEN_KEYS = {
    "accessToken",
    "legacyToken",
    "password",
    "pocketBaseToken",
    "refreshToken",
    "sessionSecret",
}


type JsonObject = dict[str, JsonValue]


class ProductParams(RootModel[JsonObject]):
    """JSON-only product parameters with credential and resource guards."""

    _allowed_fields: ClassVar[frozenset[str] | None] = None
    _required_fields: ClassVar[frozenset[str]] = frozenset()
    _field_types: ClassVar[dict[str, tuple[type, ...]]] = {}
    _catalog_example: ClassVar[JsonObject | None] = None

    @model_validator(mode="before")
    @classmethod
    def validate_product_params(cls, value: object) -> object:
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
            "relationPair",
        }
    )
    _required_fields = _allowed_fields - {"relationPair"}
    _field_types = {
        "draft": (dict, type(None)),
        "actor": (dict,),
        "relationPair": (dict, type(None)),
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
    "removes",
    "idempotencyKey",
)

# The dispatcher selects one of these method-specific DTOs.  This makes an
# unknown field, a missing required field, or a top-level type mismatch a
# JSON-RPC -32602 response before application code or HTTP is reached.
PRODUCT_RPC_REGISTRY: dict[str, type[ProductParams]] = {
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
    "schema.table.create": _closed_params(
        "SchemaTableCreateParams",
        allowed=("displayName", "operationId", "actor"),
        required=("displayName", "operationId", "actor"),
        field_types={"displayName": (str,), "operationId": (str,), "actor": (dict,)},
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
    "query.cursorOpen": _closed_params(
        "QueryCursorOpenParams",
        allowed=("tableId", "query"),
        required=("tableId", "query"),
        field_types={"tableId": (str,), "query": (dict,)},
    ),
    "query.cursorFetch": _closed_params(
        "QueryCursorFetchParams",
        allowed=("cursor",),
        required=("cursor",),
        field_types={"cursor": (str,)},
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
        allowed=("tableId", "field"),
        required=("tableId", "field"),
        field_types={"tableId": (str,), "field": (dict,)},
    ),
    "formula.draft.validate": _closed_params(
        "FormulaDraftValidateParams",
        allowed=("tableId", "displaySource"),
        required=("tableId", "displaySource"),
        field_types={"tableId": (str,), "displaySource": (str,)},
    ),
    "formula.preview": _closed_params(
        "FormulaPreviewParams",
        allowed=("tableId", "field", "row", "changedFieldIds"),
        required=("tableId", "field", "row", "changedFieldIds"),
        field_types={
            "tableId": (str,),
            "field": (dict,),
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
        allowed=("relationId", "query", "offset", "limit"),
        required=("relationId",),
        field_types={
            "relationId": (str,),
            "query": (str,),
            "offset": (int,),
            "limit": (int,),
        },
    ),
    "relation.createTarget": _closed_params(
        "RelationCreateTargetParams",
        allowed=("relationId", "label", "values", "idempotencyKey"),
        required=("relationId", "idempotencyKey"),
        field_types={
            "relationId": (str,),
            "label": (str,),
            "values": (dict,),
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


def _validate_value(value: object, depth: int) -> None:
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


__all__ = [
    "PRODUCT_RPC_REGISTRY",
    "FieldChangeApplyParams",
    "FieldChangePlanParams",
    "JsonObject",
    "JsonValue",
    "ProductParams",
]
