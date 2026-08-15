"""Application-owned seam for renderer-reachable product RPC methods."""

from __future__ import annotations

from typing import Protocol

from backend.contracts.product_rpc import JsonObject, ProductParams


class ProductRpc(Protocol):
    """Invoke one method from the closed product RPC registry."""

    async def invoke(self, method: str, params: ProductParams) -> JsonObject: ...


__all__ = ["ProductRpc"]
