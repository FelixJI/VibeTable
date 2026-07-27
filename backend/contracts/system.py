"""System-domain contracts: protocol constants + handshake DTOs.

These models are the *on-the-wire* shape of the ``system.handshake`` JSON-RPC
method. Field aliases use ``camelCase`` (the language-neutral contract), while
the Python attribute names stay ``snake_case``. ``populate_by_name=True`` lets
us validate raw payloads that use either form.
"""

from __future__ import annotations

from typing import Final

from pydantic import BaseModel, ConfigDict
from pydantic.alias_generators import to_camel

#: Protocol version negotiated by the ``system.handshake`` method. Both sides
#: must agree on this value before any other RPC method is allowed.
PROTOCOL_VERSION: Final[str] = "1.0"

#: Backend (Python) implementation version reported by ``system.handshake``.
BACKEND_VERSION: Final[str] = "0.1.0"


class HandshakeParams(BaseModel):
    """Parameters accepted by ``system.handshake``.

    Wire form::

        {"clientVersion": "X.Y.Z", "protocolVersion": "1.0"}
    """

    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )

    client_version: str
    protocol_version: str


class HandshakeResult(BaseModel):
    """Result returned by ``system.handshake``.

    Serialized with ``camelCase`` aliases so it matches the fixture byte-for-byte.
    """

    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )

    backend_version: str
    protocol_version: str
    capabilities: list[str]
