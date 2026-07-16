"""Directus WebSocket authentication, subscription and event normalization."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Awaitable, Callable
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.errors import DirectusSessionError, DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile


class RealtimeConnection(Protocol):
    async def send(self, message: str) -> None: ...

    async def recv(self) -> str: ...

    async def close(self) -> None: ...


class RealtimeConnector(Protocol):
    async def connect(self, url: str) -> RealtimeConnection: ...


class WebsocketsConnector:
    """Production connector backed by the pinned ``websockets`` runtime."""

    def __init__(
        self,
        *,
        open_timeout: float = 10.0,
        max_size: int = 2 * 1024 * 1024,
        connect_factory: Callable[..., Awaitable[Any]] | None = None,
    ) -> None:
        self._open_timeout = open_timeout
        self._max_size = max_size
        self._connect_factory = connect_factory

    async def connect(self, url: str) -> RealtimeConnection:
        factory = self._connect_factory
        if factory is None:
            from websockets.asyncio.client import connect

            factory = connect
        connection = await factory(
            url,
            open_timeout=self._open_timeout,
            max_size=self._max_size,
            ping_interval=None,
        )
        return _WebsocketsConnection(connection)


class _WebsocketsConnection:
    def __init__(self, connection: Any) -> None:
        self._connection = connection

    async def send(self, message: str) -> None:
        await self._connection.send(message)

    async def recv(self) -> str:
        message = await self._connection.recv()
        if isinstance(message, bytes):
            return message.decode("utf-8")
        if not isinstance(message, str):
            raise DirectusTransportError("Directus Realtime returned unsupported frame")
        return message

    async def close(self) -> None:
        await self._connection.close()


class DirectusChangeEvent(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        alias_generator=to_camel,
    )

    uid: str
    collection: str
    event: Literal["create", "update", "delete"]
    data: list[Any]
    invalidate_query: bool = True


class SubscriptionSpec(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    uid: str = Field(min_length=1, max_length=128)
    collection: str
    fields: list[str] = Field(min_length=1, max_length=64)
    filter: dict[str, Any] | None = None


class DirectusRealtimeSession:
    """One authenticated connection with explicit uid and field allowlists."""

    def __init__(self, connection: RealtimeConnection) -> None:
        self._connection = connection
        self._subscriptions: dict[str, SubscriptionSpec] = {}

    async def authenticate(self, access_token: str) -> None:
        await self._send({"type": "auth", "access_token": access_token})
        response = await self._receive_json()
        if response.get("type") != "auth" or response.get("status") != "ok":
            raise DirectusSessionError("Directus Realtime authentication failed")

    async def subscribe(self, profile: CollectionProfile, spec: SubscriptionSpec) -> None:
        if spec.collection != profile.collection:
            raise ValueError("subscription collection does not match profile")
        if spec.uid in self._subscriptions:
            raise ValueError("subscription uid must be unique per connection")
        denied = set(spec.fields) - set(profile.fields)
        if denied:
            raise ValueError("subscription requested fields outside profile allowlist")
        query: dict[str, Any] = {"fields": spec.fields}
        if spec.filter is not None:
            query["filter"] = spec.filter
        await self._send(
            {
                "type": "subscribe",
                "collection": profile.collection,
                "uid": spec.uid,
                "query": query,
            }
        )
        self._subscriptions[spec.uid] = spec

    async def unsubscribe(self, uid: str) -> None:
        if uid not in self._subscriptions:
            return
        await self._send({"type": "unsubscribe", "uid": uid})
        self._subscriptions.pop(uid, None)

    async def receive(self) -> DirectusChangeEvent | None:
        message = await self._receive_json()
        if message.get("type") == "ping":
            await self._send({"type": "pong"})
            return None
        if message.get("type") != "subscription" or message.get("event") == "init":
            return None
        uid = message.get("uid")
        event = message.get("event")
        if not isinstance(uid, str) or uid not in self._subscriptions:
            return None
        if event not in {"create", "update", "delete"}:
            return None
        raw_data = message.get("data")
        data = raw_data if isinstance(raw_data, list) else [raw_data]
        spec = self._subscriptions[uid]
        return DirectusChangeEvent(
            uid=uid,
            collection=spec.collection,
            event=event,
            data=data,
        )

    async def close(self) -> None:
        await self._connection.close()
        self._subscriptions.clear()

    async def _send(self, payload: dict[str, Any]) -> None:
        await self._connection.send(json.dumps(payload, separators=(",", ":")))

    async def _receive_json(self) -> dict[str, Any]:
        raw = await self._connection.recv()
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            raise DirectusTransportError("Directus Realtime returned invalid JSON") from None
        if not isinstance(payload, dict):
            raise DirectusTransportError("Directus Realtime returned invalid message")
        return payload


class DirectusRealtimeSupervisor:
    """Reconnects, reauthenticates and resubscribes with bounded backoff."""

    def __init__(
        self,
        connector: RealtimeConnector,
        auth: DirectusAuthBroker,
        url: str,
        *,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        self._connector = connector
        self._auth = auth
        self._url = url
        self._sleep = sleep

    async def run(
        self,
        subscriptions: list[tuple[CollectionProfile, SubscriptionSpec]],
        emit: Callable[[DirectusChangeEvent], Awaitable[None]],
        stop: asyncio.Event,
    ) -> None:
        attempt = 0
        while not stop.is_set():
            session: DirectusRealtimeSession | None = None
            try:
                connection = await self._connector.connect(self._url)
                session = DirectusRealtimeSession(connection)
                await session.authenticate(await self._auth.access_token())
                for profile, spec in subscriptions:
                    await session.subscribe(profile, spec)
                attempt = 0
                while not stop.is_set():
                    event = await session.receive()
                    if event is not None:
                        await emit(event)
            except (OSError, DirectusTransportError, DirectusSessionError):
                attempt += 1
                await self._sleep(min(30.0, 0.5 * (2 ** min(attempt, 6))))
            finally:
                if session is not None:
                    await session.close()
