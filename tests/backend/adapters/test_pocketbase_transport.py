from __future__ import annotations

from collections.abc import Callable, Coroutine
from pathlib import Path

import httpx
import pytest

from backend.adapters.pocketbase import transport as subject
from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import (
    PocketBaseConfig,
    PocketBaseTransportError,
    StdlibPocketBaseTransport,
)

SECRET = "a" * 64
Handler = Callable[[httpx.Request], Coroutine[None, None, httpx.Response]]


def transport(handler: Handler) -> StdlibPocketBaseTransport:
    return StdlibPocketBaseTransport(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET, 2.5),
        http_transport=httpx.MockTransport(handler),
    )


@pytest.mark.parametrize(
    "url",
    [
        "https://127.0.0.1:8090",
        "http://localhost:8090",
        "http://127.0.0.1",
        "http://127.0.0.1:8090/api",
        "http://127.0.0.1:8090?x=1",
        "http://127.0.0.1:8090/#fragment",
    ],
)
def test_config_rejects_noncanonical_loopback_origins(url: str) -> None:
    with pytest.raises(ValueError, match="IPv4 loopback"):
        PocketBaseConfig(url, SECRET)


@pytest.mark.parametrize("secret", ["", "a" * 63, "g" * 64, "a" * 65])
def test_config_rejects_invalid_secret(secret: str) -> None:
    with pytest.raises(ValueError, match="256 bits"):
        PocketBaseConfig("http://127.0.0.1:8090", secret)


@pytest.mark.parametrize("timeout", [0, -1])
def test_config_rejects_nonpositive_timeout(timeout: float) -> None:
    with pytest.raises(ValueError, match="positive"):
        PocketBaseConfig("http://127.0.0.1:8090", SECRET, timeout)


def test_transport_error_exposes_stable_rpc_code() -> None:
    error = PocketBaseTransportError("nope", code="custom.code")
    assert str(error) == "nope"
    assert error.rpc_error_data == {"code": "custom.code"}


@pytest.mark.asyncio
async def test_request_builds_canonical_json_and_repeated_query() -> None:
    captured: httpx.Request | None = None

    async def handler(request: httpx.Request) -> httpx.Response:
        nonlocal captured
        captured = request
        return httpx.Response(200, json={"ok": True})

    result = await transport(handler).request(
        "post",
        "api/test",
        query={"action": ["insert", "update"], "offset": 2},
        json_body={"title": "中文"},
        headers={"X-Test": "yes"},
    )

    assert result == {"ok": True}
    assert captured is not None
    assert captured.method == "POST"
    assert captured.url.params.get_list("action") == ["insert", "update"]
    assert captured.url.params["offset"] == "2"
    assert await captured.aread() == '{"title":"中文"}'.encode()
    assert captured.headers["Content-Type"] == "application/json"
    assert captured.headers["X-Test"] == "yes"


@pytest.mark.parametrize(
    ("body", "expected"),
    [(b"", None), (b"null", None), (b"[1,2]", [1, 2])],
)
@pytest.mark.asyncio
async def test_request_decodes_valid_responses(body: bytes, expected: object) -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=body)

    assert await transport(handler).request("GET", "/x") == expected


@pytest.mark.parametrize("body", [b"{", b"\xff"])
@pytest.mark.asyncio
async def test_request_rejects_invalid_json(body: bytes) -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=body)

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request("GET", "/x")
    assert caught.value.code == "sidecar.invalid_response"


@pytest.mark.asyncio
async def test_request_rejects_response_size(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(subject, "_MAX_RESPONSE_BYTES", 3)

    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"1234")

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request("GET", "/x")
    assert caught.value.code == "sidecar.response_too_large"


@pytest.mark.asyncio
async def test_request_rejects_unexpected_success_status() -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(201, json={})

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request("GET", "/x")
    assert caught.value.code == "sidecar.unexpected_status"


@pytest.mark.asyncio
async def test_request_maps_structured_http_error() -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(
            409,
            json={"code": "row.conflict", "message": "changed", "retryable": True},
        )

    with pytest.raises(PocketBaseProductError) as caught:
        await transport(handler).request("GET", "/x")
    assert caught.value.status == 409
    assert caught.value.code == "row.conflict"
    assert caught.value.retryable is True


@pytest.mark.parametrize("body", [b"", b"{", b'{"code":1}', b"x" * 10])
def test_http_error_falls_back_to_transport_error(
    monkeypatch: pytest.MonkeyPatch, body: bytes
) -> None:
    monkeypatch.setattr(subject, "_MAX_RESPONSE_BYTES", 4)
    error = subject._http_error(500, body)
    assert isinstance(error, PocketBaseTransportError)
    assert error.code == "sidecar.request_failed"


@pytest.mark.asyncio
async def test_request_maps_network_failures() -> None:
    async def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("offline", request=request)

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request("GET", "/x")
    assert caught.value.code == "sidecar.unavailable"


@pytest.mark.asyncio
async def test_multipart_streams_files_and_uses_safe_filename(tmp_path: Path) -> None:
    source = tmp_path / "a-b.txt"
    source.write_bytes(b"contents")
    captured_body = b""
    captured_headers: httpx.Headers | None = None

    async def handler(request: httpx.Request) -> httpx.Response:
        nonlocal captured_body, captured_headers
        captured_body = await request.aread()
        captured_headers = request.headers
        return httpx.Response(201, json={"stored": True})

    result = await transport(handler).request_multipart(
        "/upload",
        json_body={"row": "1"},
        uploads=[("file_1", str(source))],
        headers={"X-Session": "s"},
        expected_status=(201,),
    )

    assert result == {"stored": True}
    assert b'name="request"' in captured_body
    assert b'name="upload:file_1"' in captured_body
    assert b'filename="a-b.txt"' in captured_body
    assert b"contents" in captured_body
    assert captured_headers is not None
    assert captured_headers["Content-Type"].startswith("multipart/form-data; boundary=")


@pytest.mark.parametrize("uploads", [[], [("x", "p")] * 33])
@pytest.mark.asyncio
async def test_multipart_rejects_upload_count(uploads: list[tuple[str, str]]) -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        raise AssertionError("invalid upload must not be sent")

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request_multipart("/x", json_body={}, uploads=uploads)
    assert caught.value.code == "attachment.host_files_invalid"


@pytest.mark.asyncio
async def test_multipart_rejects_handle_and_invalid_file(tmp_path: Path) -> None:
    source = tmp_path / "source.txt"
    source.write_text("x", encoding="utf-8")

    async def handler(_: httpx.Request) -> httpx.Response:
        raise AssertionError("invalid upload must not be sent")

    client = transport(handler)
    with pytest.raises(PocketBaseTransportError) as handle:
        await client.request_multipart("/x", json_body={}, uploads=[("bad handle", str(source))])
    assert handle.value.code == "attachment.host_files_invalid"
    with pytest.raises(PocketBaseTransportError) as path:
        await client.request_multipart("/x", json_body={}, uploads=[("good", "relative.txt")])
    assert path.value.code == "attachment.host_file_invalid"


@pytest.mark.asyncio
async def test_multipart_rejects_oversized_file(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    source = tmp_path / "large.bin"
    source.write_bytes(b"12345")
    monkeypatch.setattr(subject, "_MAX_MULTIPART_BYTES", 4)

    async def handler(_: httpx.Request) -> httpx.Response:
        raise AssertionError("oversized upload must not be sent")

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(handler).request_multipart(
            "/x", json_body={}, uploads=[("good", str(source))]
        )
    assert caught.value.code == "attachment.host_files_too_large"


@pytest.mark.asyncio
async def test_download_writes_atomically(tmp_path: Path) -> None:
    target = tmp_path / "download.bin"
    captured: httpx.Request | None = None

    async def handler(request: httpx.Request) -> httpx.Response:
        nonlocal captured
        captured = request
        return httpx.Response(200, content=b"download")

    size = await transport(handler).download_to_file(
        "/download",
        query={"name": ["a", "b"]},
        target_path=str(target),
        headers={"X-Session": "s"},
        maximum_bytes=20,
    )

    assert size == 8
    assert target.read_bytes() == b"download"
    assert captured is not None
    assert captured.url.params.get_list("name") == ["a", "b"]
    assert captured.headers["Accept"] == "application/octet-stream"
    assert not list(tmp_path.glob("*.part"))


@pytest.mark.asyncio
async def test_download_rejects_limits_targets_status_and_size(tmp_path: Path) -> None:
    target = tmp_path / "out.bin"

    async def success(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"xxx")

    client = transport(success)
    with pytest.raises(PocketBaseTransportError) as limit:
        await client.download_to_file("/x", query={}, target_path=str(target), maximum_bytes=0)
    assert limit.value.code == "attachment.host_target_invalid"
    with pytest.raises(PocketBaseTransportError) as target_error:
        await client.download_to_file("/x", query={}, target_path="relative", maximum_bytes=2)
    assert target_error.value.code == "attachment.host_target_invalid"

    async def unexpected(_: httpx.Request) -> httpx.Response:
        return httpx.Response(201, content=b"x")

    with pytest.raises(PocketBaseTransportError) as status:
        await transport(unexpected).download_to_file(
            "/x", query={}, target_path=str(target), maximum_bytes=2
        )
    assert status.value.code == "sidecar.unexpected_status"

    with pytest.raises(PocketBaseTransportError) as size:
        await client.download_to_file("/x", query={}, target_path=str(target), maximum_bytes=2)
    assert size.value.code == "attachment.download_too_large"
    assert not target.exists()
    assert not list(tmp_path.glob("*.part"))


@pytest.mark.asyncio
async def test_download_maps_http_and_network_errors(tmp_path: Path) -> None:
    target = tmp_path / "out.bin"

    async def product_error(_: httpx.Request) -> httpx.Response:
        return httpx.Response(400, json={"code": "bad.download", "message": "bad"})

    with pytest.raises(PocketBaseProductError):
        await transport(product_error).download_to_file(
            "/x", query={}, target_path=str(target), maximum_bytes=10
        )

    async def unavailable(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("offline", request=request)

    with pytest.raises(PocketBaseTransportError) as caught:
        await transport(unavailable).download_to_file(
            "/x", query={}, target_path=str(target), maximum_bytes=10
        )
    assert caught.value.code == "sidecar.unavailable"


@pytest.mark.parametrize("raw", ["", "relative", 123, None])
def test_regular_file_and_output_file_reject_invalid_values(raw: object, tmp_path: Path) -> None:
    with pytest.raises(PocketBaseTransportError):
        subject._regular_file(raw)  # type: ignore[arg-type]
    with pytest.raises(PocketBaseTransportError):
        subject._output_file(raw)  # type: ignore[arg-type]


def test_path_helpers_accept_safe_values(tmp_path: Path) -> None:
    source = tmp_path / "file.txt"
    source.write_text("x", encoding="utf-8")
    assert subject._regular_file(str(source)) == source.resolve()
    assert subject._output_file(str(tmp_path / "new.bin")) == tmp_path / "new.bin"
    assert subject._multipart_filename('a"b.txt') == 'a"b.txt'


@pytest.mark.parametrize("name", ["", "a" * 256, "bad\nname"])
def test_multipart_filename_rejects_invalid_names(name: str) -> None:
    with pytest.raises(PocketBaseTransportError) as caught:
        subject._multipart_filename(name)
    assert caught.value.code == "attachment.host_file_invalid"
