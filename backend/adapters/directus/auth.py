"""Local-account Directus session broker; tokens never cross the RPC boundary."""

from __future__ import annotations

import time
from collections.abc import Callable
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel

from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.errors import DirectusSessionError, DirectusTransportError
from backend.adapters.directus.secrets import SecretStore
from backend.adapters.directus.transport import DirectusTransport


def _dto_config() -> ConfigDict:
    return ConfigDict(extra="forbid", frozen=True, populate_by_name=True, alias_generator=to_camel)


class CurrentUser(BaseModel):
    model_config = _dto_config()

    id: str
    display_name: str
    avatar_file_id: str | None = None
    role_id: str | None = None
    capabilities: list[str] = Field(default_factory=list)


class SessionStatus(BaseModel):
    model_config = _dto_config()

    state: Literal["anonymous", "authenticated", "expired", "unavailable"]
    expires_at: float | None = None
    user: CurrentUser | None = None


class DirectusAuthBroker:
    """Owns access/refresh tokens and exposes only safe session DTOs."""

    def __init__(
        self,
        config: DirectusSourceConfig,
        transport: DirectusTransport,
        secrets: SecretStore,
        *,
        clock: Callable[[], float] = time.time,
        refresh_skew_seconds: float = 30.0,
    ) -> None:
        self._config = config
        self._transport = transport
        self._secrets = secrets
        self._clock = clock
        self._refresh_skew_seconds = refresh_skew_seconds
        self._access_token: str | None = None
        self._expires_at: float | None = None
        self._user: CurrentUser | None = None

    async def login(self, email: str, password: str, otp: str | None = None) -> SessionStatus:
        body: dict[str, Any] = {"email": email, "password": password, "mode": "json"}
        if otp:
            body["otp"] = otp
        payload = await self._transport.request("POST", "/auth/login", json_body=body)
        self._apply_tokens(payload)
        self._user = await self.current_user(force_refresh=True)
        return self.status()

    async def access_token(self) -> str:
        if (
            self._access_token
            and self._expires_at
            and self._clock() < self._expires_at - self._refresh_skew_seconds
        ):
            return self._access_token
        await self.refresh()
        if self._access_token is None:
            raise DirectusSessionError("Directus session is unavailable")
        return self._access_token

    async def refresh(self) -> SessionStatus:
        refresh_token = self._secrets.get(self._config.token_ref)
        if not refresh_token:
            self._clear_memory()
            raise DirectusSessionError("Directus session has expired")
        try:
            payload = await self._transport.request(
                "POST",
                "/auth/refresh",
                json_body={"refresh_token": refresh_token, "mode": "json"},
            )
        except DirectusTransportError as exc:
            if exc.status in {401, 403}:
                self._secrets.delete(self._config.token_ref)
                self._clear_memory()
                raise DirectusSessionError("Directus session has expired") from exc
            raise
        self._apply_tokens(payload)
        return self.status()

    async def logout(self) -> SessionStatus:
        refresh_token = self._secrets.get(self._config.token_ref)
        try:
            if refresh_token:
                await self._transport.request(
                    "POST",
                    "/auth/logout",
                    json_body={"refresh_token": refresh_token, "mode": "json"},
                    expected_status=(200, 204),
                )
        finally:
            self._secrets.delete(self._config.token_ref)
            self._clear_memory()
        return self.status()

    async def current_user(self, *, force_refresh: bool = False) -> CurrentUser:
        if self._user is not None and not force_refresh:
            return self._user
        token = await self.access_token()
        payload = await self._transport.request(
            "GET",
            "/users/me",
            query={"fields": ["id", "first_name", "last_name", "email", "avatar", "role"]},
            access_token=token,
        )
        data = _response_data(payload)
        permissions_payload = await self._transport.request(
            "GET",
            "/permissions/me",
            access_token=token,
        )
        display_name = " ".join(
            str(data.get(key) or "").strip() for key in ("first_name", "last_name")
        ).strip() or str(data.get("email") or data["id"])
        self._user = CurrentUser(
            id=str(data["id"]),
            display_name=display_name,
            avatar_file_id=_optional_id(data.get("avatar")),
            role_id=_optional_id(data.get("role")),
            capabilities=_permission_capabilities(permissions_payload),
        )
        return self._user

    def status(self) -> SessionStatus:
        if self._access_token is None:
            state: Literal["anonymous", "authenticated", "expired", "unavailable"] = "anonymous"
        elif self._expires_at is not None and self._clock() >= self._expires_at:
            state = "expired"
        else:
            state = "authenticated"
        return SessionStatus(state=state, expires_at=self._expires_at, user=self._user)

    def _apply_tokens(self, payload: Any) -> None:
        data = _response_data(payload)
        access_token = data.get("access_token")
        refresh_token = data.get("refresh_token")
        expires = data.get("expires")
        if not isinstance(access_token, str) or not access_token:
            raise DirectusSessionError("Directus authentication response omitted access token")
        if not isinstance(refresh_token, str) or not refresh_token:
            raise DirectusSessionError("Directus authentication response omitted refresh token")
        if not isinstance(expires, (int, float)) or expires <= 0:
            raise DirectusSessionError("Directus authentication response omitted expiry")
        self._access_token = access_token
        self._expires_at = self._clock() + float(expires) / 1000.0
        self._secrets.set(self._config.token_ref, refresh_token)

    def _clear_memory(self) -> None:
        self._access_token = None
        self._expires_at = None
        self._user = None


def _response_data(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
        raise DirectusSessionError("Directus returned an invalid response")
    return payload["data"]


def _optional_id(value: Any) -> str | None:
    if isinstance(value, dict):
        value = value.get("id")
    return None if value is None else str(value)


def _permission_capabilities(payload: Any) -> list[str]:
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, dict):
        raise DirectusSessionError("Directus returned invalid current-user permissions")
    capabilities: list[str] = []
    for collection, actions in data.items():
        if not isinstance(collection, str) or not isinstance(actions, dict):
            continue
        for action, details in actions.items():
            if not isinstance(action, str) or not isinstance(details, dict):
                continue
            if details.get("access") not in {None, "none", False}:
                capabilities.append(f"{collection}.{action}")
    return sorted(capabilities)
