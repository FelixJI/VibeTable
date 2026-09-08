"""Query, schema, mutation, formula, and reconciliation product RPC module."""

from __future__ import annotations

from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcHandler,
    _object,
    _path_segment,
    _result_object,
    _text,
)
from backend.contracts.product_rpc import JsonObject, ProductParams
from backend.contracts.schema_v2 import (
    FormulaPreviewRequestV2,
    FormulaValidateRequestV2,
)


class ProductQuerySchemaRpc:
    """Owns query/schema request interpretation and sidecar projections."""

    def __init__(self, context: PocketBaseProductContext) -> None:
        self._context = context
        self._handlers: dict[str, ProductRpcHandler] = {
            "field.settings.describe": self._describe_field_settings,
            "field.change.plan": self._plan_field_change,
            "field.change.apply": self._apply_field_change,
            "field.change.status": self._field_change_status,
            "field.change.cancel": self._cancel_field_change,
            "field.recycleBin.list": self._list_recycled_fields,
            "schema.table.create": self._create_schema_table,
            "schema.delete": self._delete_schema,
            "query.validateSnapshot": self._validate_snapshot,
            "mutation.preview": self._preview_mutation,
            "mutation.apply": self._apply_mutation,
            "formula.validate": self._validate_formula,
            "formula.draft.validate": self._validate_formula_draft,
            "formula.preview": self._preview_formula,
        }
        self.methods = frozenset(self._handlers)

    async def invoke(self, method: str, params: ProductParams) -> JsonObject:
        try:
            handler = self._handlers[method]
        except KeyError as exc:
            raise ValueError(f"unknown query/schema RPC method: {method}") from exc
        return await handler(params)

    async def _create_schema_table(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/schema/tables", params.root)

    async def _delete_schema(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v1/schema/delete", params.root)

    async def _describe_field_settings(self, params: ProductParams) -> JsonObject:
        table_id = _path_segment(_text(params.root, "tableId"))
        query: dict[str, str] = {}
        if "fieldId" in params.root:
            query["fieldId"] = _text(params.root, "fieldId")
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-settings/{table_id}",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _plan_field_change(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/field-change/plan", params.root)

    async def _apply_field_change(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/field-change/apply", params.root)

    async def _field_change_status(self, params: ProductParams) -> JsonObject:
        job_id = _path_segment(_text(params.root, "jobId"))
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-change/status/{job_id}",
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _cancel_field_change(self, params: ProductParams) -> JsonObject:
        job_id = _path_segment(_text(params.root, "jobId"))
        return await self._context.post(
            f"/api/vibetable/v2/field-change/cancel/{job_id}",
            {},
        )

    async def _list_recycled_fields(self, params: ProductParams) -> JsonObject:
        table_id = _path_segment(_text(params.root, "tableId"))
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-recycle-bin/{table_id}",
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _validate_snapshot(self, params: ProductParams) -> JsonObject:
        raw = params.root
        body: JsonObject = {"snapshot": _object(raw, "snapshot")}
        if "currentQuery" in raw:
            body["currentQuery"] = _object(raw, "currentQuery")
        return await self._context.post("/api/vibetable/v1/query/validate-snapshot", body)

    async def _preview_mutation(self, params: ProductParams) -> JsonObject:
        return await self._context.client.preview_mutation(params.root)

    async def _apply_mutation(self, params: ProductParams) -> JsonObject:
        return await self._context.client.apply_mutation(params.root)

    async def _validate_formula(self, params: ProductParams) -> JsonObject:
        FormulaValidateRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/validate", params.root)

    async def _validate_formula_draft(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v1/formulas/draft/validate", params.root)

    async def _preview_formula(self, params: ProductParams) -> JsonObject:
        FormulaPreviewRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/preview", params.root)


_JSON_FILTER_OPERATORS = ("contains",)
_NULL_FILTER_OPERATORS = ("is_null", "is_not_null")


__all__ = ["ProductQuerySchemaRpc"]
