"""Formula product RPC module; schema and field-change routes are Go-owned."""

from __future__ import annotations

from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcHandler,
)
from backend.contracts.product_rpc import JsonObject, ProductParams
from backend.contracts.schema_v2 import (
    FormulaPreviewRequestV2,
    FormulaValidateRequestV2,
)


class ProductQuerySchemaRpc:
    """Owns formula request interpretation and sidecar projections."""

    def __init__(self, context: PocketBaseProductContext) -> None:
        self._context = context
        self._handlers: dict[str, ProductRpcHandler] = {
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

    async def _validate_formula(self, params: ProductParams) -> JsonObject:
        FormulaValidateRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/validate", params.root)

    async def _validate_formula_draft(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v1/formulas/draft/validate", params.root)

    async def _preview_formula(self, params: ProductParams) -> JsonObject:
        FormulaPreviewRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/preview", params.root)


__all__ = ["ProductQuerySchemaRpc"]
