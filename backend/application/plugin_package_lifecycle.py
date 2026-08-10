"""Application-owned seam for plugin package lifecycle tasks."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

from backend.contracts.plugin import PluginManifest, PluginSourceType


@dataclass(frozen=True, slots=True)
class PluginPackageInspection:
    """Normalized package facts needed to build an install plan."""

    source_type: PluginSourceType
    source_location: str
    package_hash: str
    manifest: PluginManifest


class PluginPackageLifecycle(Protocol):
    """Inspect, retain and clean up plugin packages as task-level operations."""

    def inspect(self, source_location: str) -> PluginPackageInspection: ...

    def retain(self, *, source_location: str, expected_hash: str) -> str: ...

    def is_available(self, retained_location: str) -> bool: ...

    def discard(self, retained_location: str) -> None: ...


__all__ = ["PluginPackageInspection", "PluginPackageLifecycle"]
