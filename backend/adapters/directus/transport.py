"""Async, injectable HTTP transport for the Directus REST API."""

from __future__ import annotations

import asyncio
import json
import ssl
from collections.abc import Mapping, Sequence
from typing import Any, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.errors import DirectusTransportError


class DirectusTransport(Protocol):
    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any | None = None,
        access_token: str | None = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any: ...


class StdlibDirectusTransport:
    """Small REST transport that keeps secrets in headers and errors sanitized."""

    def __init__(self, config: DirectusSourceConfig) -> None:
        self._config = config

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any | None = None,
        access_token: str | None = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any:
        return await asyncio.to_thread(
            self._request_sync,
            method,
            path,
            query,
            json_body,
            access_token,
            headers,
            tuple(expected_status),
        )

    def _request_sync(
        self,
        method: str,
        path: str,
        query: Mapping[str, Any] | None,
        json_body: Any | None,
        access_token: str | None,
        headers: Mapping[str, str] | None,
        expected_status: tuple[int, ...],
    ) -> Any:
        normalized_path = "/" + path.lstrip("/")
        url = self._config.url + normalized_path
        if query:
            url += "?" + urlencode(
                [(key, _encode_query_value(value)) for key, value in query.items()],
                quote_via=quote,
            )
        request_headers = {
            "Accept": "application/json",
            "User-Agent": "VibeTable-Next/directus.adapter.v1",
        }
        if json_body is not None:
            request_headers["Content-Type"] = "application/json"
        if access_token:
            request_headers["Authorization"] = f"Bearer {access_token}"
        if headers:
            request_headers.update(headers)
        body = None if json_body is None else json.dumps(json_body).encode("utf-8")
        request = Request(url, data=body, headers=request_headers, method=method.upper())
        context = None
        if not self._config.verify_tls:
            context = ssl._create_unverified_context()
        try:
            with urlopen(
                request,
                timeout=self._config.request_timeout_seconds,
                context=context,
            ) as response:
                raw = response.read()
                if response.status not in expected_status:
                    raise DirectusTransportError(
                        "Directus returned an unexpected status",
                        status=response.status,
                    )
        except HTTPError as exc:
            raise _map_http_error(exc.code, exc.read()) from None
        except (URLError, TimeoutError, OSError) as exc:
            raise DirectusTransportError(
                f"Directus is unreachable ({type(exc).__name__})",
                code="SERVICE_UNAVAILABLE",
            ) from None
        if not raw:
            return None
        try:
            return json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise DirectusTransportError("Directus returned invalid JSON") from None


def _encode_query_value(value: Any) -> str:
    if isinstance(value, (dict, list, tuple)):
        return json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def _map_http_error(status: int, raw: bytes) -> DirectusTransportError:
    message = "Directus request failed"
    code: str | None = None
    field_errors: dict[str, str] = {}
    try:
        payload = json.loads(raw) if raw else {}
    except (UnicodeDecodeError, json.JSONDecodeError):
        payload = {}
    errors = payload.get("errors") if isinstance(payload, dict) else None
    if isinstance(errors, list) and errors and isinstance(errors[0], dict):
        first = errors[0]
        raw_message = first.get("message")
        extensions = first.get("extensions")
        if isinstance(raw_message, str) and raw_message:
            message = raw_message
        if isinstance(extensions, dict):
            raw_code = extensions.get("code")
            if isinstance(raw_code, str):
                code = raw_code
            raw_field = extensions.get("field")
            if isinstance(raw_field, str):
                field_errors[raw_field] = message
    return DirectusTransportError(
        message,
        status=status,
        code=code,
        field_errors=field_errors,
    )
