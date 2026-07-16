from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus import DirectusAuthBroker, DirectusSessionError, DirectusSourceConfig
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.secrets import MemorySecretStore


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _config() -> DirectusSourceConfig:
    return DirectusSourceConfig(
        url="https://directus.example.test",
        project="test",
        token_ref="directus:test-user",
    )


@pytest.mark.asyncio
async def test_login_stores_only_refresh_token_and_returns_safe_current_user() -> None:
    transport = FakeTransport(
        [
            {
                "data": {
                    "access_token": "access-secret",
                    "refresh_token": "refresh-secret",
                    "expires": 60_000,
                }
            },
            {
                "data": {
                    "id": "user-1",
                    "first_name": "Ada",
                    "last_name": "Lovelace",
                    "email": "ada@example.test",
                    "avatar": "file-1",
                    "role": {"id": "role-1"},
                }
            },
            {
                "data": {
                    "vibetable_demo": {
                        "read": {"access": "full", "fields": ["id", "name"]},
                        "update": {"access": "none", "fields": []},
                    }
                }
            },
        ]
    )
    secrets = MemorySecretStore()
    broker = DirectusAuthBroker(_config(), transport, secrets, clock=lambda: 100.0)

    status = await broker.login("ada@example.test", "password")

    assert status.state == "authenticated"
    assert status.expires_at == 160.0
    assert status.user is not None
    assert status.user.display_name == "Ada Lovelace"
    assert status.user.capabilities == ["vibetable_demo.read"]
    assert secrets.get("directus:test-user") == "refresh-secret"
    assert "access-secret" not in status.model_dump_json()
    assert "refresh-secret" not in status.model_dump_json()
    assert transport.requests[1]["access_token"] == "access-secret"
    assert transport.requests[2]["path"] == "/permissions/me"


@pytest.mark.asyncio
async def test_expired_access_token_refreshes_once_and_rotates_refresh_token() -> None:
    transport = FakeTransport(
        [
            {
                "data": {
                    "access_token": "new-access",
                    "refresh_token": "new-refresh",
                    "expires": 120_000,
                }
            }
        ]
    )
    secrets = MemorySecretStore()
    secrets.set("directus:test-user", "old-refresh")
    broker = DirectusAuthBroker(_config(), transport, secrets, clock=lambda: 50.0)

    assert await broker.access_token() == "new-access"
    assert secrets.get("directus:test-user") == "new-refresh"
    assert transport.requests[0]["json_body"] == {
        "refresh_token": "old-refresh",
        "mode": "json",
    }


@pytest.mark.asyncio
async def test_invalid_refresh_clears_secret_and_exposes_no_token() -> None:
    transport = FakeTransport([DirectusTransportError("denied", status=401, code="INVALID_TOKEN")])
    secrets = MemorySecretStore()
    secrets.set("directus:test-user", "stale-refresh")
    broker = DirectusAuthBroker(_config(), transport, secrets)

    with pytest.raises(DirectusSessionError, match="expired"):
        await broker.refresh()

    assert secrets.get("directus:test-user") is None
    assert broker.status().state == "anonymous"


@pytest.mark.asyncio
async def test_logout_revokes_refresh_and_always_clears_local_state() -> None:
    transport = FakeTransport([None])
    secrets = MemorySecretStore()
    secrets.set("directus:test-user", "refresh")
    broker = DirectusAuthBroker(_config(), transport, secrets)

    status = await broker.logout()

    assert status.state == "anonymous"
    assert secrets.get("directus:test-user") is None
    assert transport.requests[0]["path"] == "/auth/logout"
