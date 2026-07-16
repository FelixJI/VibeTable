"""Application service for protocol negotiation and capability discovery."""

from __future__ import annotations

from collections.abc import Callable, Iterable

from backend.contracts.system import (
    BACKEND_VERSION,
    PROTOCOL_VERSION,
    HandshakeParams,
    HandshakeResult,
)


class ProtocolMismatchError(Exception):
    """Raised when a client requests a protocol version the backend cannot serve.

    Mapped by the dispatcher to JSON-RPC code ``-32001`` with
    ``data.kind == "protocol_mismatch"``.
    """


class SystemService:
    """Implements the ``system.*`` JSON-RPC methods."""

    def __init__(
        self,
        capabilities: Callable[[], Iterable[str]] | None = None,
    ) -> None:
        self._capabilities = capabilities or (lambda: ("system.handshake",))

    async def handshake(self, params: HandshakeParams) -> HandshakeResult:
        """Negotiate the protocol version and advertise registered methods.

        Raises ``ProtocolMismatchError`` if the client's requested protocol
        version differs from ``PROTOCOL_VERSION``.
        """
        if params.protocol_version != PROTOCOL_VERSION:
            raise ProtocolMismatchError(
                f"Client requested protocol {params.protocol_version!r}, "
                f"backend speaks {PROTOCOL_VERSION!r}"
            )
        return HandshakeResult(
            backend_version=BACKEND_VERSION,
            protocol_version=PROTOCOL_VERSION,
            capabilities=sorted(set(self._capabilities())),
        )
