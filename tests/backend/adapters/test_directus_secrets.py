from __future__ import annotations

import sys
from pathlib import Path

import pytest

from backend.adapters.directus.secrets import DpapiFileSecretStore, Win32DpapiProtector


class ReversingProtector:
    def protect(self, value: bytes) -> bytes:
        return b"protected:" + value[::-1]

    def unprotect(self, value: bytes) -> bytes:
        assert value.startswith(b"protected:")
        return value.removeprefix(b"protected:")[::-1]


def test_dpapi_file_store_uses_hashed_reference_and_protected_payload(tmp_path: Path) -> None:
    store = DpapiFileSecretStore(tmp_path, ReversingProtector())

    store.set("directus:prod/user@example.test", "refresh-secret")

    files = list(tmp_path.iterdir())
    assert len(files) == 1
    assert "directus" not in files[0].name
    assert b"refresh-secret" not in files[0].read_bytes()
    assert store.get("directus:prod/user@example.test") == "refresh-secret"

    store.delete("directus:prod/user@example.test")
    assert store.get("directus:prod/user@example.test") is None


@pytest.mark.skipif(sys.platform != "win32", reason="Windows DPAPI product boundary")
def test_win32_dpapi_round_trip() -> None:
    protector = Win32DpapiProtector()
    secret = b"VibeTable managed login test secret"

    protected = protector.protect(secret)

    assert protected != secret
    assert protector.unprotect(protected) == secret
