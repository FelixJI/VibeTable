"""PocketBase sidecar product-API adapters."""

from backend.adapters.pocketbase.client import (
    PocketBaseClient,
    PocketBaseProductError,
    QueryPageResult,
)
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.adapters.pocketbase.realtime import (
    PocketBaseRealtimeSession,
    PocketBaseRealtimeSupervisor,
    ProductEvent,
    StdlibSSEConnector,
)
from backend.adapters.pocketbase.transport import (
    PocketBaseConfig,
    PocketBaseTransportError,
    StdlibPocketBaseTransport,
)

__all__ = [
    "PocketBaseBulkMutationClient",
    "PocketBaseClient",
    "PocketBaseConfig",
    "PocketBaseProductError",
    "PocketBaseRealtimeSession",
    "PocketBaseRealtimeSupervisor",
    "PocketBaseTransportError",
    "ProductEvent",
    "QueryPageResult",
    "StdlibPocketBaseTransport",
    "StdlibSSEConnector",
]
