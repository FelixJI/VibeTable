from __future__ import annotations

import json

import pytest

from backend.adapters.directus.profile import CollectionProfile
from backend.adapters.directus.realtime import (
    DirectusRealtimeSession,
    SubscriptionSpec,
    WebsocketsConnector,
)


class FakeConnection:
    def __init__(self, incoming: list[dict]) -> None:
        self.incoming = [json.dumps(item) for item in incoming]
        self.sent: list[dict] = []
        self.closed = False

    async def send(self, message: str) -> None:
        self.sent.append(json.loads(message))

    async def recv(self) -> str:
        return self.incoming.pop(0)

    async def close(self) -> None:
        self.closed = True


def _profile() -> CollectionProfile:
    return CollectionProfile(
        collection="contracts",
        fields=["id", "number", "status", "date_updated"],
        create_fields=["id", "number", "status"],
        update_fields=["number", "status"],
    )


@pytest.mark.asyncio
async def test_auth_subscribe_ping_and_change_event_lifecycle() -> None:
    connection = FakeConnection(
        [
            {"type": "auth", "status": "ok"},
            {"type": "ping"},
            {
                "type": "subscription",
                "event": "update",
                "uid": "contracts-main",
                "data": [{"id": "1", "status": "active"}],
            },
        ]
    )
    session = DirectusRealtimeSession(connection)

    await session.authenticate("access-secret")
    await session.subscribe(
        _profile(),
        SubscriptionSpec(
            uid="contracts-main",
            collection="contracts",
            fields=["id", "status"],
        ),
    )
    assert await session.receive() is None
    event = await session.receive()

    assert connection.sent[0] == {"type": "auth", "access_token": "access-secret"}
    assert connection.sent[1]["type"] == "subscribe"
    assert connection.sent[2] == {"type": "pong"}
    assert event is not None
    assert event.event == "update"
    assert event.invalidate_query is True
    assert event.model_dump(by_alias=True)["invalidateQuery"] is True


@pytest.mark.asyncio
async def test_subscription_rejects_duplicate_uid_and_unapproved_fields() -> None:
    connection = FakeConnection([])
    session = DirectusRealtimeSession(connection)
    spec = SubscriptionSpec(
        uid="contracts-main",
        collection="contracts",
        fields=["id"],
    )
    await session.subscribe(_profile(), spec)

    with pytest.raises(ValueError, match="unique"):
        await session.subscribe(_profile(), spec)
    with pytest.raises(ValueError, match="allowlist"):
        await session.subscribe(
            _profile(),
            SubscriptionSpec(
                uid="contracts-secret",
                collection="contracts",
                fields=["internal_secret"],
            ),
        )


@pytest.mark.asyncio
async def test_websockets_connector_disables_library_ping_and_decodes_binary_frame() -> None:
    raw_connection = FakeConnection([])
    raw_connection.incoming.append(b'{"type":"ping"}')
    captured: dict = {}

    async def connect_factory(url: str, **kwargs: object) -> FakeConnection:
        captured["url"] = url
        captured.update(kwargs)
        return raw_connection

    connector = WebsocketsConnector(connect_factory=connect_factory)
    connection = await connector.connect("wss://directus.example.test/websocket")

    assert await connection.recv() == '{"type":"ping"}'
    assert captured["url"] == "wss://directus.example.test/websocket"
    assert captured["ping_interval"] is None
    assert captured["max_size"] == 2 * 1024 * 1024
