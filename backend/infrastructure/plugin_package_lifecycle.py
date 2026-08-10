"""Local filesystem adapter for the plugin package lifecycle seam."""

from __future__ import annotations

import base64
import contextlib
import os
import shutil
import uuid
from pathlib import Path

from backend.application.plugin_package_lifecycle import (
    PluginPackageInspection,
    PluginPackageLifecycle,
)
from backend.contracts.plugin import PluginManifest
from backend.infrastructure.plugin_package import inspect_plugin_package, pack_plugin


def _filesystem_package_path(path: Path) -> Path:
    """Return a filesystem-safe absolute path without weakening package identity."""

    resolved = path.resolve()
    raw = str(resolved)
    if os.name != "nt" or raw.startswith("\\\\?\\") or len(raw) < 260:
        return resolved
    if raw.startswith("\\\\"):
        return Path(f"\\\\?\\UNC\\{raw[2:]}")
    return Path(f"\\\\?\\{raw}")


class LocalPluginPackageLifecycle(PluginPackageLifecycle):
    """Own content-addressed package caching and filesystem cleanup."""

    def __init__(self, cache_root: Path) -> None:
        self._cache_root = cache_root.resolve()

    def inspect(self, source_location: str) -> PluginPackageInspection:
        source = Path(source_location).resolve()
        inspected = inspect_plugin_package(source)
        return PluginPackageInspection(
            source_type="local-folder" if source.is_dir() else "package",
            source_location=str(source),
            package_hash=inspected.package_hash,
            manifest=PluginManifest.model_validate(inspected.manifest),
        )

    def retain(self, *, source_location: str, expected_hash: str) -> str:
        digest = expected_hash.removeprefix("sha256:")
        # Lowercase Base32 retains all digest bits while keeping long Windows
        # cache paths shorter than their hexadecimal equivalent.
        compact_digest = (
            base64.b32encode(bytes.fromhex(digest)).rstrip(b"=").decode("ascii").lower()
        )
        destination = self._cache_root / f"{compact_digest}.vtplugin"
        filesystem_destination = _filesystem_package_path(destination)
        destination.parent.mkdir(parents=True, exist_ok=True)
        if filesystem_destination.is_file():
            if inspect_plugin_package(filesystem_destination).package_hash != expected_hash:
                raise ValueError("retained plugin package failed integrity verification")
            return str(filesystem_destination)

        temporary = destination.with_name(f".tmp-{uuid.uuid4().hex[:16]}.vtplugin")
        filesystem_temporary = _filesystem_package_path(temporary)
        try:
            source = Path(source_location).resolve()
            if source.is_dir():
                retained_hash = pack_plugin(source, filesystem_temporary)
            else:
                shutil.copyfile(source, filesystem_temporary)
                retained_hash = inspect_plugin_package(filesystem_temporary).package_hash
            if retained_hash != expected_hash:
                raise ValueError("retained plugin package hash does not match the plan")
            filesystem_temporary.replace(filesystem_destination)
        finally:
            filesystem_temporary.unlink(missing_ok=True)
        return str(filesystem_destination)

    def is_available(self, retained_location: str) -> bool:
        return Path(retained_location).resolve().is_file()

    def discard(self, retained_location: str) -> None:
        with contextlib.suppress(OSError):
            Path(retained_location).unlink()


__all__ = ["LocalPluginPackageLifecycle"]
