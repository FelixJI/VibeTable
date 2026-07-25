from __future__ import annotations

import io
import json
from pathlib import Path
from urllib.error import HTTPError, URLError

import pytest

from backend.adapters.pocketbase import transport as subject
from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.transport import (
    PocketBaseConfig,
    PocketBaseTransportError,
    StdlibPocketBaseTransport,
)

SECRET = "a" * 64


class FakeResponse:
    def __init__(self, body: bytes = b"", *, status: int = 200) -> None:
        self.status = status
        self._stream = io.BytesIO(body)

    def read(self, size: int = -1) -> bytes:
        return self._stream.read(size)

    def __enter__(self) -> FakeResponse:
        return self

    def __exit__(self, *_args: object) -> None:
        return None


def transport() -> StdlibPocketBaseTransport:
    return StdlibPocketBaseTransport(
        PocketBaseConfig("http://127.0.0.1:8090", SECRET, 2.5)
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
async def test_async_request_builds_canonical_json_and_repeated_query(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    captured = {}

    def fake_urlopen(request, *, timeout):
        captured.update(request=request, timeout=timeout)
        return FakeResponse(b'{"ok":true}')

    monkeypatch.setattr(subject, "urlopen", fake_urlopen)
    result = await transport().request(
        "post",
        "api/test",
        query={"action": ["insert", "update"], "offset": 2},
        json_body={"title": "中文"},
        headers={"X-Test": "yes"},
    )
    request = captured["request"]
    assert result == {"ok": True}
    assert request.full_url.endswith("action=insert&action=update&offset=2")
    assert request.method == "POST"
    assert json.loads(request.data) == {"title": "中文"}
    assert request.headers["Content-type"] == "application/json"
    assert request.headers["X-test"] == "yes"
    assert captured["timeout"] == 2.5


@pytest.mark.parametrize(
    ("body", "expected"),
    [(b"", None), (b"null", None), (b"[1,2]", [1, 2])],
)
def test_request_sync_decodes_valid_responses(
    monkeypatch: pytest.MonkeyPatch,
    body: bytes,
    expected: object,
) -> None:
    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(body))
    assert transport()._request_sync("GET", "/x", {}, None, {}, (200,)) == expected


@pytest.mark.parametrize("body", [b"{", b"\xff"])
def test_request_sync_rejects_invalid_json(
    monkeypatch: pytest.MonkeyPatch, body: bytes
) -> None:
    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(body))
    with pytest.raises(PocketBaseTransportError) as caught:
        transport()._request_sync("GET", "/x", {}, None, {}, (200,))
    assert caught.value.code == "sidecar.invalid_response"


def test_request_sync_rejects_size_and_status(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(subject, "_MAX_RESPONSE_BYTES", 3)
    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b"1234"))
    with pytest.raises(PocketBaseTransportError) as size:
        transport()._request_sync("GET", "/x", {}, None, {}, (200,))
    assert size.value.code == "sidecar.response_too_large"

    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b"{}", status=201))
    with pytest.raises(PocketBaseTransportError) as status:
        transport()._request_sync("GET", "/x", {}, None, {}, (200,))
    assert status.value.code == "sidecar.unexpected_status"


def _http_error(status: int, body: bytes) -> HTTPError:
    return HTTPError("http://127.0.0.1:8090/x", status, "failed", {}, io.BytesIO(body))


def test_request_sync_maps_structured_http_error(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    payload = b'{"code":"row.conflict","message":"changed","retryable":true}'
    monkeypatch.setattr(
        subject, "urlopen", lambda *_args, **_kwargs: (_ for _ in ()).throw(_http_error(409, payload))
    )
    with pytest.raises(PocketBaseProductError) as caught:
        transport()._request_sync("GET", "/x", {}, None, {}, (200,))
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


@pytest.mark.parametrize("exception", [URLError("offline"), TimeoutError(), OSError()])
def test_request_sync_maps_io_failures(
    monkeypatch: pytest.MonkeyPatch, exception: Exception
) -> None:
    monkeypatch.setattr(
        subject, "urlopen", lambda *_args, **_kwargs: (_ for _ in ()).throw(exception)
    )
    with pytest.raises(PocketBaseTransportError) as caught:
        transport()._request_sync("GET", "/x", {}, None, {}, (200,))
    assert caught.value.code == "sidecar.unavailable"


@pytest.mark.asyncio
async def test_multipart_posts_files_and_sanitized_filename(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    source = tmp_path / "a-b.txt"
    source.write_bytes(b"contents")
    captured = {}

    def fake_urlopen(request, *, timeout):
        captured.update(request=request, timeout=timeout)
        return FakeResponse(b'{"stored":true}', status=201)

    monkeypatch.setattr(subject, "urlopen", fake_urlopen)
    result = await transport().request_multipart(
        "/upload",
        json_body={"row": "1"},
        uploads=[("file_1", str(source))],
        headers={"X-Session": "s"},
        expected_status=(201,),
    )
    body = captured["request"].data
    assert result == {"stored": True}
    assert b'name="request"' in body
    assert b'name="upload:file_1"' in body
    assert b'filename="a-b.txt"' in body
    assert body.endswith(b"--\r\n")


@pytest.mark.parametrize("uploads", [[], [("x", "p")] * 33])
def test_multipart_rejects_upload_count(
    uploads: list[tuple[str, str]],
) -> None:
    with pytest.raises(PocketBaseTransportError) as caught:
        transport()._request_multipart_sync("/x", {}, tuple(uploads), {}, (200,))
    assert caught.value.code == "attachment.host_files_invalid"


def test_multipart_rejects_handle_and_invalid_file(tmp_path: Path) -> None:
    source = tmp_path / "source.txt"
    source.write_text("x", encoding="utf-8")
    with pytest.raises(PocketBaseTransportError) as handle:
        transport()._request_multipart_sync(
            "/x", {}, (("bad handle", str(source)),), {}, (200,)
        )
    assert handle.value.code == "attachment.host_files_invalid"
    with pytest.raises(PocketBaseTransportError) as path:
        transport()._request_multipart_sync(
            "/x", {}, (("good", "relative.txt"),), {}, (200,)
        )
    assert path.value.code == "attachment.host_file_invalid"


def test_multipart_rejects_oversized_file(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    source = tmp_path / "large.bin"
    source.write_bytes(b"12345")
    monkeypatch.setattr(subject, "_MAX_MULTIPART_BYTES", 4)
    with pytest.raises(PocketBaseTransportError) as caught:
        transport()._request_multipart_sync(
            "/x", {}, (("good", str(source)),), {}, (200,)
        )
    assert caught.value.code == "attachment.host_files_too_large"


def test_request_bytes_success_and_failure_mapping(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b""))
    assert transport()._request_bytes_sync("POST", "/x", b"x", {}, (200,)) is None
    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b"{"))
    with pytest.raises(PocketBaseTransportError) as invalid:
        transport()._request_bytes_sync("POST", "/x", b"x", {}, (200,))
    assert invalid.value.code == "sidecar.invalid_response"


@pytest.mark.asyncio
async def test_download_writes_atomically(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "download.bin"
    captured = {}

    def fake_urlopen(request, *, timeout):
        captured.update(request=request, timeout=timeout)
        return FakeResponse(b"download")

    monkeypatch.setattr(subject, "urlopen", fake_urlopen)
    size = await transport().download_to_file(
        "/download",
        query={"name": ["a", "b"]},
        target_path=str(target),
        headers={"X-Session": "s"},
        maximum_bytes=20,
    )
    assert size == 8
    assert target.read_bytes() == b"download"
    assert "name=a&name=b" in captured["request"].full_url
    assert captured["request"].headers["Accept"] == "application/octet-stream"


def test_download_rejects_limits_targets_status_and_size(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "out.bin"
    with pytest.raises(PocketBaseTransportError) as limit:
        transport()._download_to_file_sync("/x", {}, str(target), {}, (200,), 0)
    assert limit.value.code == "attachment.host_target_invalid"
    with pytest.raises(PocketBaseTransportError) as target_error:
        transport()._download_to_file_sync("/x", {}, "relative", {}, (200,), 2)
    assert target_error.value.code == "attachment.host_target_invalid"

    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b"x", status=201))
    with pytest.raises(PocketBaseTransportError) as status:
        transport()._download_to_file_sync("/x", {}, str(target), {}, (200,), 2)
    assert status.value.code == "sidecar.unexpected_status"

    monkeypatch.setattr(subject, "urlopen", lambda *_args, **_kwargs: FakeResponse(b"xxx"))
    with pytest.raises(PocketBaseTransportError) as size:
        transport()._download_to_file_sync("/x", {}, str(target), {}, (200,), 2)
    assert size.value.code == "attachment.download_too_large"
    assert not target.exists()
    assert not list(tmp_path.glob("*.part"))


def test_download_maps_http_and_io_errors(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    target = tmp_path / "out.bin"
    monkeypatch.setattr(
        subject,
        "urlopen",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            _http_error(400, b'{"code":"bad.download","message":"bad"}')
        ),
    )
    with pytest.raises(PocketBaseProductError):
        transport()._download_to_file_sync("/x", {}, str(target), {}, (200,), 10)

    monkeypatch.setattr(
        subject, "urlopen", lambda *_args, **_kwargs: (_ for _ in ()).throw(URLError("x"))
    )
    with pytest.raises(PocketBaseTransportError) as unavailable:
        transport()._download_to_file_sync("/x", {}, str(target), {}, (200,), 10)
    assert unavailable.value.code == "sidecar.unavailable"


@pytest.mark.parametrize("raw", ["", "relative", 123, None])
def test_regular_file_and_output_file_reject_invalid_values(raw: object, tmp_path: Path) -> None:
    with pytest.raises(PocketBaseTransportError):
        subject._regular_file(raw)  # type: ignore[arg-type]
    with pytest.raises(PocketBaseTransportError):
        subject._output_file(raw)  # type: ignore[arg-type]


def test_path_helpers_accept_safe_values_and_escape_filename(tmp_path: Path) -> None:
    source = tmp_path / "file.txt"
    source.write_text("x", encoding="utf-8")
    assert subject._regular_file(str(source)) == source.resolve()
    assert subject._output_file(str(tmp_path / "new.bin")) == tmp_path / "new.bin"
    assert subject._multipart_filename('a"b.txt') == 'a\\"b.txt'


@pytest.mark.parametrize("name", ["", "a" * 256, "bad\nname"])
def test_multipart_filename_rejects_invalid_names(name: str) -> None:
    with pytest.raises(PocketBaseTransportError) as caught:
        subject._multipart_filename(name)
    assert caught.value.code == "attachment.host_file_invalid"
