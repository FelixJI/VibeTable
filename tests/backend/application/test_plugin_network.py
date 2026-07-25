from __future__ import annotations

from typing import Any

import pytest

from backend.application.plugin_network import (
    NetworkGrant,
    NetworkPolicyError,
    PluginNetworkPort,
)


class FakeTransport:
    def __init__(self, *, body: bytes = b"ok") -> None:
        self.body = body
        self.calls: list[dict[str, Any]] = []

    async def request(self, **kwargs: Any) -> tuple[int, dict[str, str], bytes]:
        self.calls.append(kwargs)
        return 200, {"content-type": "text/plain"}, self.body


def _port(
    transport: FakeTransport | None = None,
    *,
    methods: frozenset[str] = frozenset({"GET", "POST"}),
) -> tuple[PluginNetworkPort, FakeTransport]:
    fake = transport or FakeTransport()
    return (
        PluginNetworkPort(
            transport=fake,
            grant=NetworkGrant(
                domains=frozenset({"api.example.com"}),
                methods=methods,
                timeout_seconds=5,
                max_request_bytes=16,
                max_response_bytes=32,
            ),
        ),
        fake,
    )


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "url",
    [
        "http://api.example.com/data",
        "https://api.example.com.evil.test/data",
        "https://user:pass@api.example.com/data",
        "https://127.0.0.1/data",
        "https://localhost/data",
    ],
)
async def test_network_rejects_scheme_domain_credential_and_localhost_bypasses(
    url: str,
) -> None:
    port, transport = _port()

    with pytest.raises(NetworkPolicyError):
        await port.request(method="GET", url=url)

    assert transport.calls == []


@pytest.mark.asyncio
async def test_network_rejects_method_timeout_credentials_and_request_size() -> None:
    port, transport = _port(methods=frozenset({"GET"}))

    with pytest.raises(NetworkPolicyError, match="method"):
        await port.request(method="DELETE", url="https://api.example.com/data")
    with pytest.raises(NetworkPolicyError, match="timeout"):
        await port.request(
            method="GET",
            url="https://api.example.com/data",
            timeout_seconds=6,
        )
    with pytest.raises(NetworkPolicyError, match="credential"):
        await port.request(
            method="GET",
            url="https://api.example.com/data",
            headers={"Authorization": "Bearer secret"},
        )
    with pytest.raises(NetworkPolicyError, match="request"):
        await port.request(
            method="GET",
            url="https://api.example.com/data",
            body=b"x" * 17,
        )

    assert transport.calls == []


@pytest.mark.asyncio
async def test_network_disables_redirects_and_caps_response() -> None:
    port, transport = _port(FakeTransport(body=b"x" * 33))

    with pytest.raises(NetworkPolicyError, match="response"):
        await port.request(method="GET", url="https://api.example.com/data")

    assert transport.calls[0]["allow_redirects"] is False
    assert transport.calls[0]["max_response_bytes"] == 33


@pytest.mark.asyncio
async def test_network_allows_exact_granted_https_request() -> None:
    port, transport = _port()

    response = await port.request(
        method="POST",
        url="https://api.example.com/data",
        headers={"Content-Type": "application/json"},
        body=b"{}",
        timeout_seconds=2,
    )

    assert response.status == 200
    assert response.body == b"ok"
    assert transport.calls[0]["timeout_seconds"] == 2
