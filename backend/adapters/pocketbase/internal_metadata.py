"""Allowlisted adapter for PocketBase-owned internal product metadata."""

from __future__ import annotations

import math
from collections.abc import Mapping
from typing import Protocol

from backend.application.revisioned_metadata_port import (
    JsonObject,
    JsonValue,
    raise_metadata_transport_error,
)


class _InternalMetadataClient(Protocol):
    async def list_internal_metadata(self, namespace: str) -> JsonObject: ...

    async def upsert_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject: ...

    async def delete_internal_metadata(
        self,
        namespace: str,
        request: Mapping[str, JsonValue],
    ) -> JsonObject: ...


_NAMESPACES = frozenset(
    {
        "shared_settings",
        "dashboards",
        "panels",
        "presets",
        "content_versions",
        "interfaces",
    }
)


class PocketBaseInternalMetadataPort:
    """Translate logical metadata operations to fixed sidecar use cases."""

    def __init__(
        self,
        *,
        client: _InternalMetadataClient,
    ) -> None:
        self._client = client

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[JsonObject]:
        self._namespace(namespace)
        response = await self._client.list_internal_metadata(namespace)
        raw_items = response.get("items")
        if not isinstance(raw_items, list):
            raise ValueError("internal metadata list returned invalid items")
        items = [_project_item(item) for item in raw_items]
        if scope is not None:
            items = [item for item in items if item.get("scope") == scope]
        if keys:
            wanted = set(keys)
            items = [
                item for item in items if item.get("id") in wanted or item.get("key") in wanted
            ]
        return sorted(
            items,
            key=lambda item: str(item.get("key") or item.get("id") or ""),
        )

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, JsonValue],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject:
        self._writable_namespace(namespace)
        logical_id = record_id or _logical_id(values)
        current = await self._current_item(namespace, logical_id)
        revision = expected_revision
        if revision is None:
            revision = str(current.get("revision", "")) if current else ""
        payload = {
            key: value for key, value in (current or {}).items() if key not in {"id", "revision"}
        }
        payload.update(_json_object(values, "metadata values"))
        try:
            response = await self._client.upsert_internal_metadata(
                namespace,
                {
                    "logicalId": logical_id,
                    "payload": payload,
                    "expectedRevision": revision or "",
                    "idempotencyKey": idempotency_key,
                },
            )
        except Exception as error:
            raise_metadata_transport_error(error)
        item = response.get("item")
        if not isinstance(item, dict):
            raise ValueError("internal metadata mutation returned an invalid item")
        return {
            "status": response.get("status"),
            "changeSetId": response.get("changeSetId"),
            "emittedEvents": _freeze(response.get("emittedEvents", [])),
            "item": _project_item(item),
        }

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject:
        self._writable_namespace(namespace)
        revision = expected_revision
        if revision is None:
            revision = await self._current_revision(namespace, record_id)
        if not revision:
            raise ValueError("internal metadata record was not found")
        response = await self._client.delete_internal_metadata(
            namespace,
            {
                "logicalId": record_id,
                "expectedRevision": revision,
                "idempotencyKey": idempotency_key,
            },
        )
        return _json_object(response, "metadata deletion")

    async def _current_revision(self, namespace: str, logical_id: str) -> str:
        item = await self._current_item(namespace, logical_id)
        if item is None:
            return ""
        revision = item.get("revision")
        if not isinstance(revision, str) or not revision:
            raise ValueError("internal metadata revision is invalid")
        return revision

    async def _current_item(
        self,
        namespace: str,
        logical_id: str,
    ) -> JsonObject | None:
        items = await self.list_metadata(namespace, keys=[logical_id])
        return items[0] if items else None

    @staticmethod
    def _writable_namespace(namespace: str) -> None:
        PocketBaseInternalMetadataPort._namespace(namespace)
        if namespace in {"interfaces", "dashboards", "panels"}:
            raise ValueError("legacy layout metadata is read-only")

    @staticmethod
    def _namespace(namespace: str) -> None:
        if namespace not in _NAMESPACES:
            raise ValueError(f"unknown internal metadata namespace {namespace!r}")


def _logical_id(values: Mapping[str, JsonValue]) -> str:
    for key in ("id", "key"):
        value = values.get(key)
        if isinstance(value, str) and value:
            return value
    raise ValueError("internal metadata logical id is required")


def _project_item(value: object) -> JsonObject:
    if not isinstance(value, dict):
        raise ValueError("internal metadata item is invalid")
    logical_id = value.get("logicalId")
    revision = value.get("revision")
    payload = value.get("payload")
    if (
        not isinstance(logical_id, str)
        or not logical_id
        or not isinstance(revision, str)
        or not revision
        or not isinstance(payload, dict)
    ):
        raise ValueError("internal metadata item is invalid")
    result = _json_object(payload, "internal metadata payload")
    result["id"] = logical_id
    result["revision"] = revision
    return result


def _json_object(value: object, label: str) -> JsonObject:
    frozen = _freeze(value)
    if not isinstance(frozen, dict):
        raise ValueError(f"{label} is not a JSON object")
    return frozen


def _freeze(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError("metadata value contains a non-finite number")
        return value
    if isinstance(value, list):
        return [_freeze(item) for item in value]
    if isinstance(value, Mapping):
        if not all(isinstance(key, str) for key in value):
            raise ValueError("JSON object keys must be strings")
        return {str(key): _freeze(item) for key, item in value.items()}
    raise ValueError("metadata value is not JSON-compatible")


__all__ = ["PocketBaseInternalMetadataPort"]
