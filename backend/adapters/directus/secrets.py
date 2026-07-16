"""Refresh-token storage isolated behind Windows DPAPI."""

from __future__ import annotations

import base64
import ctypes
import hashlib
import os
import sys
from ctypes import wintypes
from pathlib import Path
from typing import Protocol


class SecretStore(Protocol):
    def get(self, reference: str) -> str | None: ...

    def set(self, reference: str, value: str) -> None: ...

    def delete(self, reference: str) -> None: ...


class SecretProtector(Protocol):
    def protect(self, value: bytes) -> bytes: ...

    def unprotect(self, value: bytes) -> bytes: ...


class MemorySecretStore:
    """Test-only in-memory secret store."""

    def __init__(self) -> None:
        self._values: dict[str, str] = {}

    def get(self, reference: str) -> str | None:
        return self._values.get(reference)

    def set(self, reference: str, value: str) -> None:
        self._values[reference] = value

    def delete(self, reference: str) -> None:
        self._values.pop(reference, None)


class Win32DpapiProtector:
    """Current-user DPAPI wrapper using only the Python standard library."""

    def protect(self, value: bytes) -> bytes:
        return self._crypt(value, protect=True)

    def unprotect(self, value: bytes) -> bytes:
        return self._crypt(value, protect=False)

    @staticmethod
    def _crypt(value: bytes, *, protect: bool) -> bytes:
        if sys.platform != "win32":  # pragma: no cover - Windows product boundary
            raise RuntimeError("Windows DPAPI is unavailable on this platform")

        class DataBlob(ctypes.Structure):
            _fields_ = [
                ("cbData", wintypes.DWORD),
                ("pbData", ctypes.POINTER(ctypes.c_ubyte)),
            ]

        crypt32 = ctypes.WinDLL("crypt32", use_last_error=True)
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        blob_pointer = ctypes.POINTER(DataBlob)
        if protect:
            crypt = crypt32.CryptProtectData
            crypt.argtypes = [
                blob_pointer,
                wintypes.LPCWSTR,
                blob_pointer,
                ctypes.c_void_p,
                ctypes.c_void_p,
                wintypes.DWORD,
                blob_pointer,
            ]
        else:
            crypt = crypt32.CryptUnprotectData
            crypt.argtypes = [
                blob_pointer,
                ctypes.POINTER(wintypes.LPWSTR),
                blob_pointer,
                ctypes.c_void_p,
                ctypes.c_void_p,
                wintypes.DWORD,
                blob_pointer,
            ]
        crypt.restype = wintypes.BOOL
        kernel32.LocalFree.argtypes = [ctypes.c_void_p]
        kernel32.LocalFree.restype = ctypes.c_void_p

        buffer = ctypes.create_string_buffer(value)
        input_blob = DataBlob(
            len(value),
            ctypes.cast(buffer, ctypes.POINTER(ctypes.c_ubyte)),
        )
        output_blob = DataBlob()
        if protect:
            result = crypt(
                ctypes.byref(input_blob),
                "VibeTable Directus",
                None,
                None,
                None,
                0,
                ctypes.byref(output_blob),
            )
        else:
            result = crypt(
                ctypes.byref(input_blob),
                None,
                None,
                None,
                None,
                0,
                ctypes.byref(output_blob),
            )
        if not result:
            error = ctypes.get_last_error()
            raise OSError(error, "Windows DPAPI operation failed")
        try:
            return ctypes.string_at(output_blob.pbData, output_blob.cbData)
        finally:
            kernel32.LocalFree(output_blob.pbData)


class DpapiFileSecretStore:
    """Atomic local files containing only DPAPI-protected token bytes."""

    def __init__(self, root: Path, protector: SecretProtector | None = None) -> None:
        self._root = root
        self._protector = protector or Win32DpapiProtector()

    def get(self, reference: str) -> str | None:
        path = self._path(reference)
        if not path.exists():
            return None
        protected = base64.b64decode(path.read_bytes(), validate=True)
        return self._protector.unprotect(protected).decode("utf-8")

    def set(self, reference: str, value: str) -> None:
        self._root.mkdir(parents=True, exist_ok=True)
        path = self._path(reference)
        protected = self._protector.protect(value.encode("utf-8"))
        temporary = path.with_suffix(".tmp")
        temporary.write_bytes(base64.b64encode(protected))
        os.replace(temporary, path)

    def delete(self, reference: str) -> None:
        self._path(reference).unlink(missing_ok=True)

    def _path(self, reference: str) -> Path:
        digest = hashlib.sha256(reference.encode("utf-8")).hexdigest()
        return self._root / f"{digest}.secret"
