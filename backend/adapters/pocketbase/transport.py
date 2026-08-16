"""Bounded async HTTP transport for the private loopback sidecar."""

from __future__ import annotations

import contextlib
import json
import os
import re
import stat
import tempfile
from collections.abc import Mapping, Sequence
from contextlib import ExitStack
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx

from backend.adapters.pocketbase.client import PocketBaseProductError

_SECRET_PATTERN = re.compile(r"^[0-9a-fA-F]{64}$")
_MAX_RESPONSE_BYTES = 16 * 1024 * 1024
_MAX_MULTIPART_BYTES = 101 * 1024 * 1024
_UPLOAD_HANDLE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
_USER_AGENT = "VibeTable-Next/pocketbase.adapter.v1"


def _dbg(message: str) -> None:  # TEMPORARY E2E DEBUG LOGGING - REVERT
    import os
    import time

    target = os.environ.get("VIBETABLE_TRANSPORT_DEBUG_LOG")
    if not target:
        return
    try:
        with open(target, "a", encoding="utf-8") as handle:
            handle.write(f"{time.time():.3f} pid={os.getpid()} transport {message}\n")
    except OSError:
        pass


class PocketBaseTransportError(Exception):
    def __init__(self, message: str, *, code: str = "sidecar.unavailable") -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


@dataclass(frozen=True)
class PocketBaseConfig:
    base_url: str
    session_secret: str
    timeout_seconds: float = 30.0

    def __post_init__(self) -> None:
        parsed = urlsplit(self.base_url)
        if (
            parsed.scheme != "http"
            or parsed.hostname != "127.0.0.1"
            or parsed.port is None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError("PocketBase URL must be an IPv4 loopback HTTP origin")
        if not _SECRET_PATTERN.fullmatch(self.session_secret):
            raise ValueError("PocketBase session secret must encode exactly 256 bits")
        if self.timeout_seconds <= 0:
            raise ValueError("PocketBase timeout must be positive")


class StdlibPocketBaseTransport:
    """HTTPX-backed implementation kept under the stable adapter class name."""

    def __init__(
        self,
        config: PocketBaseConfig,
        *,
        http_transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._base_url = config.base_url.rstrip("/")
        self._timeout = config.timeout_seconds
        self._http_transport = http_transport

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any | None = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any:
        request_headers = {
            "Accept": "application/json",
            "User-Agent": _USER_AGENT,
            **dict(headers or {}),
        }
        body: bytes | None = None
        if json_body is not None:
            request_headers["Content-Type"] = "application/json"
            body = _encode_json(json_body)
        import time as _t  # TEMPORARY E2E DEBUG LOGGING - REVERT

        _t0 = _t.monotonic()
        try:
            async with (
                self._client() as client,
                client.stream(
                    method.upper(),
                    _normalized_path(path),
                    params=query,
                    content=body,
                    headers=request_headers,
                ) as response,
            ):
                raw = await _read_limited(response, _MAX_RESPONSE_BYTES)
        except PocketBaseTransportError as exc:
            _dbg(
                f"request {method} {path} query={dict(query or {})!r} "
                f"ms={(_t.monotonic() - _t0) * 1000:.0f} pbt={exc.code} {exc!r}"
            )
            raise
        except httpx.TransportError as exc:
            _dbg(
                f"request {method} {path} query={dict(query or {})!r} "
                f"ms={(_t.monotonic() - _t0) * 1000:.0f} transport_error={exc!r}"
            )
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
        return _decode_response(response.status_code, raw, tuple(expected_status))

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, Any],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any:
        if not uploads or len(uploads) > 32:
            raise PocketBaseTransportError(
                "Managed attachment upload count is invalid",
                code="attachment.host_files_invalid",
            )
        request_json = _encode_json(json_body, sort_keys=True)
        with ExitStack() as stack:
            files: list[tuple[str, Any]] = [("request", (None, request_json, "application/json"))]
            for handle, raw_path in uploads:
                if not _UPLOAD_HANDLE.fullmatch(handle):
                    raise PocketBaseTransportError(
                        "Managed attachment upload handle is invalid",
                        code="attachment.host_files_invalid",
                    )
                source = _regular_file(raw_path)
                filename = _multipart_filename(source.name)
                try:
                    stream = stack.enter_context(source.open("rb"))
                except OSError:
                    raise PocketBaseTransportError(
                        "Managed attachment file could not be read",
                        code="attachment.host_file_unreadable",
                    ) from None
                files.append((f"upload:{handle}", (filename, stream, "application/octet-stream")))

            request_headers = {
                "Accept": "application/json",
                "User-Agent": _USER_AGENT,
                **dict(headers or {}),
            }
            try:
                async with self._client() as client:
                    request = client.build_request(
                        "POST",
                        _normalized_path(path),
                        headers=request_headers,
                        files=files,
                    )
                    length = request.headers.get("Content-Length")
                    if length is None or int(length) > _MAX_MULTIPART_BYTES:
                        raise PocketBaseTransportError(
                            "Managed attachment upload is too large",
                            code="attachment.host_files_too_large",
                        )
                    response = await client.send(request, stream=True)
                    try:
                        raw = await _read_limited(response, _MAX_RESPONSE_BYTES)
                    finally:
                        await response.aclose()
            except PocketBaseTransportError:
                raise
            except httpx.TransportError:
                raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
            except OSError:
                raise PocketBaseTransportError(
                    "Managed attachment file could not be read",
                    code="attachment.host_file_unreadable",
                ) from None
        return _decode_response(response.status_code, raw, tuple(expected_status))

    async def download_to_file(
        self,
        path: str,
        *,
        query: Mapping[str, Any],
        target_path: str,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
        maximum_bytes: int = 2 * 1024 * 1024 * 1024,
    ) -> int:
        if maximum_bytes <= 0:
            raise PocketBaseTransportError(
                "Managed attachment size limit is invalid",
                code="attachment.host_target_invalid",
            )
        target = _output_file(target_path)
        request_headers = {
            "Accept": "application/octet-stream",
            "User-Agent": _USER_AGENT,
            **dict(headers or {}),
        }
        temporary: str | None = None
        import time as _t  # TEMPORARY E2E DEBUG LOGGING - REVERT

        _t0 = _t.monotonic()
        _dbg(f"download {path} query={dict(query or {})!r} target={target_path}")
        try:
            async with (
                self._client() as client,
                client.stream(
                    "GET",
                    _normalized_path(path),
                    params=query,
                    headers=request_headers,
                ) as response,
            ):
                _dbg(
                    f"download headers status={response.status_code} "
                    f"ms={(_t.monotonic() - _t0) * 1000:.0f} "
                    f"resp_headers={dict(response.headers)!r}"
                )
                if response.status_code not in expected_status:
                    raw = await _read_limited(response, _MAX_RESPONSE_BYTES)
                    raise _status_error(response.status_code, raw)
                import os as _os  # TEMPORARY E2E DEBUG LOGGING - REVERT

                _dbg(
                    f"download mkstemp dir={target.parent!r} isdir={_os.path.isdir(target.parent)}"
                )
                descriptor, temporary = tempfile.mkstemp(
                    prefix=".vibetable-attachment-",
                    suffix=".part",
                    dir=str(target.parent),
                )
                _dbg(f"download mkstemp ok temp={temporary!r} exists={_os.path.exists(temporary)}")
                total = 0
                with os.fdopen(descriptor, "wb") as output:
                    async for chunk in response.aiter_bytes(1024 * 1024):
                        total += len(chunk)
                        if total > maximum_bytes:
                            raise PocketBaseTransportError(
                                "Managed attachment download is too large",
                                code="attachment.download_too_large",
                            )
                        output.write(chunk)
                    output.flush()
                    os.fsync(output.fileno())
                _dbg(f"download streamed total={total} temp_exists={_os.path.exists(temporary)}")
            import os as _os2  # TEMPORARY E2E DEBUG LOGGING - REVERT

            _dbg(
                f"download pre-replace temp={temporary!r} exists={_os2.path.exists(temporary)} "
                f"target={target!r} parent_isdir={_os2.path.isdir(target.parent)}"
            )
            os.replace(temporary, target)
            temporary = None
            _dbg(f"download ok bytes={total} ms={(_t.monotonic() - _t0) * 1000:.0f}")
            return total
        except PocketBaseTransportError as exc:
            _dbg(f"download pbt={exc.code} {exc!r} ms={(_t.monotonic() - _t0) * 1000:.0f}")
            raise
        except httpx.TransportError as exc:
            _dbg(f"download transport_error={exc!r} ms={(_t.monotonic() - _t0) * 1000:.0f}")
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
        except OSError as exc:
            _dbg(f"download os_error={exc!r} ms={(_t.monotonic() - _t0) * 1000:.0f}")
            raise PocketBaseTransportError(
                "Managed attachment could not be saved",
                code="attachment.host_target_unwritable",
            ) from None
        finally:
            if temporary is not None:
                with contextlib.suppress(OSError):
                    os.unlink(temporary)

    def _client(self) -> httpx.AsyncClient:
        return httpx.AsyncClient(
            base_url=self._base_url,
            timeout=self._timeout,
            transport=self._http_transport,
            trust_env=False,
        )


async def _read_limited(response: httpx.Response, maximum_bytes: int) -> bytes:
    body = bytearray()
    async for chunk in response.aiter_bytes():
        body.extend(chunk)
        if len(body) > maximum_bytes:
            raise PocketBaseTransportError(
                "PocketBase response exceeded the safe size limit",
                code="sidecar.response_too_large",
            )
    return bytes(body)


def _decode_response(status: int, raw: bytes, expected_status: tuple[int, ...]) -> Any:
    if status not in expected_status:
        raise _status_error(status, raw)
    if not raw:
        return None
    try:
        return json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise PocketBaseTransportError(
            "PocketBase returned invalid JSON",
            code="sidecar.invalid_response",
        ) from None


def _status_error(status: int, raw: bytes) -> Exception:
    if status >= 400:
        return _http_error(status, raw)
    return PocketBaseTransportError(
        "PocketBase returned an unexpected status",
        code="sidecar.unexpected_status",
    )


def _encode_json(value: Any, *, sort_keys: bool = False) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=sort_keys,
    ).encode("utf-8")


def _normalized_path(path: str) -> str:
    return "/" + path.lstrip("/")


def _http_error(status: int, raw: bytes) -> Exception:
    try:
        payload = json.loads(raw) if raw and len(raw) <= _MAX_RESPONSE_BYTES else {}
    except (UnicodeDecodeError, json.JSONDecodeError):
        payload = {}
    if isinstance(payload, dict) and isinstance(payload.get("code"), str):
        return PocketBaseProductError(status=status, payload=payload)
    return PocketBaseTransportError(
        "PocketBase request failed",
        code="sidecar.request_failed",
    )


def _regular_file(raw_path: str) -> Path:
    try:
        if not isinstance(raw_path, str) or not raw_path:
            raise OSError
        source = Path(raw_path)
        if not source.is_absolute():
            raise OSError
        source = source.resolve(strict=True)
        metadata = source.stat()
        if not stat.S_ISREG(metadata.st_mode):
            raise OSError
        if metadata.st_size < 0 or metadata.st_size > _MAX_MULTIPART_BYTES:
            raise PocketBaseTransportError(
                "Managed attachment upload is too large",
                code="attachment.host_files_too_large",
            )
        return source
    except PocketBaseTransportError:
        raise
    except (OSError, RuntimeError):
        raise PocketBaseTransportError(
            "Managed attachment file is invalid",
            code="attachment.host_file_invalid",
        ) from None


def _output_file(raw_path: str) -> Path:
    try:
        if not isinstance(raw_path, str) or not raw_path:
            raise OSError
        target = Path(raw_path)
        if not target.is_absolute() or target.name in {"", ".", ".."}:
            raise OSError
        parent = target.parent.resolve(strict=True)
        if not parent.is_dir():
            raise OSError
        return parent / target.name
    except (OSError, RuntimeError):
        raise PocketBaseTransportError(
            "Managed attachment destination is invalid",
            code="attachment.host_target_invalid",
        ) from None


def _multipart_filename(value: str) -> str:
    name = Path(value).name
    if not name or len(name) > 255 or any(ord(character) < 32 for character in name):
        raise PocketBaseTransportError(
            "Managed attachment filename is invalid",
            code="attachment.host_file_invalid",
        )
    return name


__all__ = [
    "PocketBaseConfig",
    "PocketBaseTransportError",
    "StdlibPocketBaseTransport",
]
