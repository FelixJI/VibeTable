"""PocketBase adapter for the closed product RPC seam."""

from __future__ import annotations

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseTransport
from backend.adapters.pocketbase.product_query_schema_rpc import ProductQuerySchemaRpc
from backend.adapters.pocketbase.product_relation_lookup_file_rpc import (
    ProductRelationLookupFileRpc,
)
from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcModule,
)
from backend.application.product_rpc import ProductRpc
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, ProductParams


class PocketBaseProductRpc(ProductRpc):
    """Dispatches the closed product registry to cohesive PocketBase modules."""

    def __init__(
        self,
        *,
        client: PocketBaseClient,
        transport: PocketBaseTransport,
        session_secret: str,
    ) -> None:
        if not session_secret:
            raise ValueError("PocketBase session secret is required")
        context = PocketBaseProductContext(
            client=client,
            transport=transport,
            headers={"X-VibeTable-Session": session_secret},
        )
        modules: tuple[ProductRpcModule, ...] = (
            ProductQuerySchemaRpc(context),
            ProductRelationLookupFileRpc(context),
        )
        module_by_method: dict[str, ProductRpcModule] = {}
        for module in modules:
            for method in module.methods:
                if method in module_by_method:
                    raise RuntimeError(f"duplicate PocketBase product RPC route: {method}")
                module_by_method[method] = module
        if module_by_method.keys() != PRODUCT_RPC_REGISTRY.keys():
            raise RuntimeError("PocketBase product RPC routes do not match the contract registry")
        self._module_by_method = module_by_method

    async def invoke(self, method: str, params: ProductParams) -> JsonObject:
        """Invoke one closed product route after dispatcher parameter validation."""

        try:
            module = self._module_by_method[method]
        except KeyError as exc:
            raise ValueError(f"unknown product RPC method: {method}") from exc
        return await module.invoke(method, params)


__all__ = ["PocketBaseProductRpc"]
