"""Directus-first data, authentication, schema and realtime adapter boundary."""

from .auth import CurrentUser, DirectusAuthBroker, SessionStatus
from .client import DirectusClient
from .contracts import DirectusCapabilities, DirectusSourceConfig
from .errors import (
    DirectusAdapterError,
    DirectusQueryError,
    DirectusSchemaError,
    DirectusSessionError,
    DirectusTransportError,
)
from .profile import CapabilityManifest, CollectionProfile, RelationProfile
from .query import DirectusQueryPlan, compile_directus_query
from .realtime import (
    DirectusChangeEvent,
    DirectusRealtimeSession,
    SubscriptionSpec,
    WebsocketsConnector,
)
from .schema import DirectusSchemaPlan, build_directus_schema

__all__ = [
    "CapabilityManifest",
    "CollectionProfile",
    "CurrentUser",
    "DirectusAdapterError",
    "DirectusAuthBroker",
    "DirectusCapabilities",
    "DirectusChangeEvent",
    "DirectusClient",
    "DirectusQueryError",
    "DirectusQueryPlan",
    "DirectusRealtimeSession",
    "DirectusSchemaError",
    "DirectusSchemaPlan",
    "DirectusSessionError",
    "DirectusSourceConfig",
    "DirectusTransportError",
    "RelationProfile",
    "SessionStatus",
    "SubscriptionSpec",
    "WebsocketsConnector",
    "build_directus_schema",
    "compile_directus_query",
]
