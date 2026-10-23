"""Production conflict error shared by the Go-owned plugin catalog adapter."""

from __future__ import annotations


class PluginStoreConflictError(Exception):
    pass


__all__ = ["PluginStoreConflictError"]
