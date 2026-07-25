"""Bounded stdlib HTTP transport for the private loopback sidecar."""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import re
import secrets
import stat
import tempfile
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen

from backend.adapters.pocketbase.client import PocketBaseProductError

_SECRET_PATTERN = re.compile(r"^[0-9a-fA-F]{64}$")
_MAX_RESPONSE_BYTES = 16 * 1024 * 1024
_MAX_MULTIPART_BYTES = 101 * 1024 * 1024
_UPLOAD_HANDLE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


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
    def __init__(self, config: PocketBaseConfig) -> None:
        self._base_url = config.base_url.rstrip("/")
        self._timeout = config.timeout_seconds

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
        return await asyncio.to_thread(
            self._request_sync,
            method,
            path,
            dict(query or {}),
            json_body,
            dict(headers or {}),
            tuple(expected_status),
        )

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, Any],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> Any:
        return await asyncio.to_thread(
            self._request_multipart_sync,
            path,
            json_body,
            tuple(uploads),
            dict(headers or {}),
            tuple(expected_status),
        )

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
        return await asyncio.to_thread(
            self._download_to_file_sync,
            path,
            dict(query),
            target_path,
            dict(headers or {}),
            tuple(expected_status),
            maximum_bytes,
        )

    def _request_sync(
        self,
        method: str,
        path: str,
        query: dict[str, Any],
        json_body: Any | None,
        headers: dict[str, str],
        expected_status: tuple[int, ...],
    ) -> Any:
        normalized_path = "/" + path.lstrip("/")
        url = self._base_url + normalized_path
        if query:
            # Product routes such as history use repeated query parameters
            # (for example ``action=insert&action=update``). ``doseq`` keeps
            # that closed wire shape instead of serializing a Python list
            # representation into a single parameter.
            url += "?" + urlencode(query, doseq=True)
        request_headers = {
            "Accept": "application/json",
            "User-Agent": "VibeTable-Next/pocketbase.adapter.v1",
            **headers,
        }
        body: bytes | None = None
        if json_body is not None:
            request_headers["Content-Type"] = "application/json"
            body = json.dumps(
                json_body,
                ensure_ascii=False,
                allow_nan=False,
                separators=(",", ":"),
            ).encode("utf-8")
        request = Request(
            url,
            data=body,
            headers=request_headers,
            method=method.upper(),
        )
        try:
            with urlopen(request, timeout=self._timeout) as response:
                raw = response.read(_MAX_RESPONSE_BYTES + 1)
                if len(raw) > _MAX_RESPONSE_BYTES:
                    raise PocketBaseTransportError(
                        "PocketBase response exceeded the safe size limit",
                        code="sidecar.response_too_large",
                    )
                if response.status not in expected_status:
                    raise PocketBaseTransportError(
                        "PocketBase returned an unexpected status",
                        code="sidecar.unexpected_status",
                    )
        except HTTPError as exc:
            raise _http_error(exc.code, exc.read(_MAX_RESPONSE_BYTES + 1)) from None
        except (URLError, TimeoutError, OSError):
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
        if not raw:
            return None
        try:
            return json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise PocketBaseTransportError(
                "PocketBase returned invalid JSON",
                code="sidecar.invalid_response",
            ) from None

    def _request_multipart_sync(
        self,
        path: str,
        json_body: Mapping[str, Any],
        uploads: tuple[tuple[str, str], ...],
        headers: dict[str, str],
        expected_status: tuple[int, ...],
    ) -> Any:
        if not uploads or len(uploads) > 32:
            raise PocketBaseTransportError(
                "Managed attachment upload count is invalid",
                code="attachment.host_files_invalid",
            )
        boundary = "----VibeTable" + secrets.token_hex(24)
        body = bytearray()

        def append_part_header(name: str, filename: str | None = None) -> None:
            body.extend(f"--{boundary}\r\n".encode())
            disposition = f'Content-Disposition: form-data; name="{name}"'
            if filename is not None:
                disposition += f'; filename="{_multipart_filename(filename)}"'
            body.extend((disposition + "\r\n").encode("utf-8"))
            body.extend(
                (
                    "Content-Type: application/json\r\n\r\n"
                    if filename is None
                    else "Content-Type: application/octet-stream\r\n\r\n"
                ).encode()
            )

        append_part_header("request")
        body.extend(
            json.dumps(
                json_body,
                ensure_ascii=False,
                allow_nan=False,
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8")
        )
        body.extend(b"\r\n")
        for handle, raw_path in uploads:
            if not _UPLOAD_HANDLE.fullmatch(handle):
                raise PocketBaseTransportError(
                    "Managed attachment upload handle is invalid",
                    code="attachment.host_files_invalid",
                )
            source = _regular_file(raw_path)
            append_part_header(f"upload:{handle}", source.name)
            try:
                with source.open("rb") as stream:
                    while chunk := stream.read(1024 * 1024):
                        body.extend(chunk)
                        if len(body) > _MAX_MULTIPART_BYTES:
                            raise PocketBaseTransportError(
                                "Managed attachment upload is too large",
                                code="attachment.host_files_too_large",
                            )
            except PocketBaseTransportError:
                raise
            except OSError:
                raise PocketBaseTransportError(
                    "Managed attachment file could not be read",
                    code="attachment.host_file_unreadable",
                ) from None
            body.extend(b"\r\n")
        body.extend(f"--{boundary}--\r\n".encode())
        if len(body) > _MAX_MULTIPART_BYTES:
            raise PocketBaseTransportError(
                "Managed attachment upload is too large",
                code="attachment.host_files_too_large",
            )
        return self._request_bytes_sync(
            "POST",
            path,
            bytes(body),
            {
                **headers,
                "Content-Type": f"multipart/form-data; boundary={boundary}",
            },
            expected_status,
        )

    def _download_to_file_sync(
        self,
        path: str,
        query: dict[str, Any],
        target_path: str,
        headers: dict[str, str],
        expected_status: tuple[int, ...],
        maximum_bytes: int,
    ) -> int:
        if maximum_bytes <= 0:
            raise PocketBaseTransportError(
                "Managed attachment size limit is invalid",
                code="attachment.host_target_invalid",
            )
        target = _output_file(target_path)
        url = self._base_url + "/" + path.lstrip("/")
        if query:
            url += "?" + urlencode(query, doseq=True)
        request = Request(
            url,
            headers={
                "Accept": "application/octet-stream",
                "User-Agent": "VibeTable-Next/pocketbase.adapter.v1",
                **headers,
            },
            method="GET",
        )
        temporary: str | None = None
        try:
            descriptor, temporary = tempfile.mkstemp(
                prefix=".vibetable-attachment-",
                suffix=".part",
                dir=str(target.parent),
            )
            total = 0
            with os.fdopen(descriptor, "wb") as output:
                try:
                    with urlopen(request, timeout=self._timeout) as response:
                        if response.status not in expected_status:
                            raise PocketBaseTransportError(
                                "PocketBase returned an unexpected status",
                                code="sidecar.unexpected_status",
                            )
                        while chunk := response.read(1024 * 1024):
                            total += len(chunk)
                            if total > maximum_bytes:
                                raise PocketBaseTransportError(
                                    "Managed attachment download is too large",
                                    code="attachment.download_too_large",
                                )
                            output.write(chunk)
                        output.flush()
                        os.fsync(output.fileno())
                except HTTPError as exc:
                    raise _http_error(
                        exc.code,
                        exc.read(_MAX_RESPONSE_BYTES + 1),
                    ) from None
                except PocketBaseTransportError:
                    raise
                except (URLError, TimeoutError, OSError):
                    raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
            os.replace(temporary, target)
            temporary = None
            return total
        except PocketBaseTransportError:
            raise
        except OSError:
            raise PocketBaseTransportError(
                "Managed attachment could not be saved",
                code="attachment.host_target_unwritable",
            ) from None
        finally:
            if temporary is not None:
                with contextlib.suppress(OSError):
                    os.unlink(temporary)

    def _request_bytes_sync(
        self,
        method: str,
        path: str,
        body: bytes,
        headers: dict[str, str],
        expected_status: tuple[int, ...],
    ) -> Any:
        request = Request(
            self._base_url + "/" + path.lstrip("/"),
            data=body,
            headers={
                "Accept": "application/json",
                "User-Agent": "VibeTable-Next/pocketbase.adapter.v1",
                **headers,
            },
            method=method,
        )
        try:
            with urlopen(request, timeout=self._timeout) as response:
                raw = response.read(_MAX_RESPONSE_BYTES + 1)
                if len(raw) > _MAX_RESPONSE_BYTES:
                    raise PocketBaseTransportError(
                        "PocketBase response exceeded the safe size limit",
                        code="sidecar.response_too_large",
                    )
                if response.status not in expected_status:
                    raise PocketBaseTransportError(
                        "PocketBase returned an unexpected status",
                        code="sidecar.unexpected_status",
                    )
        except HTTPError as exc:
            raise _http_error(
                exc.code,
                exc.read(_MAX_RESPONSE_BYTES + 1),
            ) from None
        except PocketBaseTransportError:
            raise
        except (URLError, TimeoutError, OSError):
            raise PocketBaseTransportError("PocketBase sidecar is unavailable") from None
        if not raw:
            return None
        try:
            return json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise PocketBaseTransportError(
                "PocketBase returned invalid JSON",
                code="sidecar.invalid_response",
            ) from None


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
    return name.replace("\\", "\\\\").replace('"', '\\"')


__all__ = [
    "PocketBaseConfig",
    "PocketBaseTransportError",
    "StdlibPocketBaseTransport",
]
