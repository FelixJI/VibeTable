"""Capability-gated outbound network port for local plugins."""

from __future__ import annotations

import ipaddress
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Protocol
from urllib.parse import urlsplit

_CREDENTIAL_HEADERS = frozenset(
    {
        "authorization",
        "cookie",
        "proxy-authorization",
        "x-api-key",
    }
)


class NetworkPolicyError(ValueError):
    """A request fell outside the explicit network grant."""


@dataclass(frozen=True, slots=True)
class NetworkGrant:
    domains: frozenset[str]
    methods: frozenset[str] = frozenset({"GET"})
    timeout_seconds: float = 5
    max_request_bytes: int = 1 << 20
    max_response_bytes: int = 4 << 20

    def __post_init__(self) -> None:
        if not self.domains:
            raise ValueError("network grant must contain at least one domain")
        if self.timeout_seconds <= 0:
            raise ValueError("network timeout must be positive")
        if self.max_request_bytes < 0 or self.max_response_bytes < 1:
            raise ValueError("network size limits are invalid")


@dataclass(frozen=True, slots=True)
class NetworkResponse:
    status: int
    headers: dict[str, str]
    body: bytes


class NetworkTransport(Protocol):
    async def request(
        self,
        *,
        method: str,
        url: str,
        headers: Mapping[str, str],
        body: bytes,
        timeout_seconds: float,
        allow_redirects: bool,
        max_response_bytes: int,
    ) -> tuple[int, dict[str, str], bytes]: ...


class PluginNetworkPort:
    """Validate every request before giving it to the host transport."""

    def __init__(self, *, transport: NetworkTransport, grant: NetworkGrant) -> None:
        self._transport = transport
        self._grant = grant
        self._domains = frozenset(domain.rstrip(".").lower() for domain in grant.domains)

    async def request(
        self,
        *,
        method: str,
        url: str,
        headers: Mapping[str, str] | None = None,
        body: bytes = b"",
        timeout_seconds: float | None = None,
    ) -> NetworkResponse:
        normalized_method = method.upper()
        if normalized_method not in self._grant.methods:
            raise NetworkPolicyError("network method is not granted")

        parsed = urlsplit(url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise NetworkPolicyError("network URL must use HTTPS")
        if parsed.username is not None or parsed.password is not None:
            raise NetworkPolicyError("network URL credentials are forbidden")
        hostname = parsed.hostname.rstrip(".").lower()
        if hostname == "localhost" or _is_non_public_ip(hostname):
            raise NetworkPolicyError("network localhost and non-public IPs are forbidden")
        if hostname not in self._domains:
            raise NetworkPolicyError("network domain is not granted")
        if parsed.port not in (None, 443):
            raise NetworkPolicyError("network port is not granted")

        clean_headers = dict(headers or {})
        if any(name.lower() in _CREDENTIAL_HEADERS for name in clean_headers):
            raise NetworkPolicyError("network credential headers are forbidden")
        if len(body) > self._grant.max_request_bytes:
            raise NetworkPolicyError("network request exceeds the size grant")
        timeout = self._grant.timeout_seconds if timeout_seconds is None else timeout_seconds
        if timeout <= 0 or timeout > self._grant.timeout_seconds:
            raise NetworkPolicyError("network timeout exceeds the grant")

        status, response_headers, response_body = await self._transport.request(
            method=normalized_method,
            url=url,
            headers=clean_headers,
            body=body,
            timeout_seconds=timeout,
            allow_redirects=False,
            # A bounded transport can stop reading immediately after this
            # sentinel byte rather than buffering an unbounded response.
            max_response_bytes=self._grant.max_response_bytes + 1,
        )
        if len(response_body) > self._grant.max_response_bytes:
            raise NetworkPolicyError("network response exceeds the size grant")
        return NetworkResponse(
            status=status,
            headers=dict(response_headers),
            body=bytes(response_body),
        )


def _is_non_public_ip(hostname: str) -> bool:
    try:
        address = ipaddress.ip_address(hostname)
    except ValueError:
        return False
    return not address.is_global


__all__ = [
    "NetworkGrant",
    "NetworkPolicyError",
    "NetworkResponse",
    "NetworkTransport",
    "PluginNetworkPort",
]
