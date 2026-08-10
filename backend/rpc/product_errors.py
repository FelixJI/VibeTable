"""JSON-RPC error wiring for the PocketBase product adapter."""

from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.rpc.dispatcher import CODE_PRODUCT_DATA, register_rpc_error


def register_product_rpc_errors() -> None:
    """Register sanitized local product API and transport failures."""

    register_rpc_error(
        PocketBaseProductError,
        code=CODE_PRODUCT_DATA,
        message="Product data error",
        kind="product_data_error",
    )
    register_rpc_error(
        PocketBaseTransportError,
        code=CODE_PRODUCT_DATA,
        message="Product data unavailable",
        kind="product_data_unavailable",
    )


__all__ = ["register_product_rpc_errors"]
