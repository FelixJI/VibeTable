from __future__ import annotations

import io
import json
from typing import Any
from urllib.error import HTTPError

import pytest

from backend.adapters.directus import DirectusSourceConfig, DirectusTransportError
from backend.adapters.directus.transport import StdlibDirectusTransport


class FakeResponse:
    def __init__(self, payload: Any, status: int = 200) -> None:
        self.status = status
        self._raw = json.dumps(payload).encode("utf-8") if payload is not None else b""

    def __enter__(self) -> FakeResponse:
        return self

    def __exit__(self, *args: object) -> None:
        return None

    def read(self) -> bytes:
        return self._raw


def _transport() -> StdlibDirectusTransport:
    return StdlibDirectusTransport(
        DirectusSourceConfig(
            url="https://directus.example.test",
            project="test",
            token_ref="directus:test",
        )
    )


def test_transport_serializes_structured_query_and_keeps_token_out_of_url(monkeypatch: Any) -> None:
    captured: dict[str, Any] = {}

    def fake_urlopen(request: Any, **kwargs: Any) -> FakeResponse:
        captured["request"] = request
        captured["kwargs"] = kwargs
        return FakeResponse({"data": [{"id": "1"}]})

    monkeypatch.setattr("backend.adapters.directus.transport.urlopen", fake_urlopen)
    result = _transport()._request_sync(
        "GET",
        "/items/contracts",
        {"filter": {"status": {"_eq": "open"}}, "fields": ["id", "status"]},
        None,
        "access-secret",
        None,
        (200,),
    )

    request = captured["request"]
    assert result == {"data": [{"id": "1"}]}
    assert "access-secret" not in request.full_url
    assert request.get_header("Authorization") == "Bearer access-secret"
    assert "%7B" in request.full_url
    assert "%5B" in request.full_url


def test_transport_maps_directus_error_without_echoing_request_secret(monkeypatch: Any) -> None:
    payload = {
        "errors": [
            {
                "message": "Value is not unique",
                "extensions": {"code": "RECORD_NOT_UNIQUE", "field": "number"},
            }
        ]
    }

    def fake_urlopen(request: Any, **kwargs: Any) -> FakeResponse:
        raise HTTPError(
            request.full_url,
            400,
            "bad request",
            {},
            io.BytesIO(json.dumps(payload).encode("utf-8")),
        )

    monkeypatch.setattr("backend.adapters.directus.transport.urlopen", fake_urlopen)

    with pytest.raises(DirectusTransportError) as captured:
        _transport()._request_sync(
            "POST",
            "/items/contracts",
            None,
            {"number": "secret-business-value"},
            "access-secret",
            None,
            (200,),
        )

    error = captured.value
    assert error.status == 400
    assert error.code == "RECORD_NOT_UNIQUE"
    assert error.field_errors == {"number": "Value is not unique"}
    assert "access-secret" not in str(error)
    assert "secret-business-value" not in str(error)
