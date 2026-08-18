"""Typed revisioned metadata commands shared by application modules."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from typing import Never, Protocol, runtime_checkable

type JsonScalar = str | int | float | bool | None
type JsonValue = JsonScalar | list["JsonValue"] | dict[str, "JsonValue"]
type JsonObject = dict[str, JsonValue]


@dataclass(frozen=True)
class MetadataQuery:
    namespace: str
    keys: tuple[str, ...] = ()


@dataclass(frozen=True)
class MetadataWrite:
    namespace: str
    logical_id: str
    values: JsonObject
    expected_revision: str | None
    idempotency_key: str


@dataclass(frozen=True)
class MetadataDelete:
    namespace: str
    logical_id: str
    expected_revision: str
    idempotency_key: str


@dataclass(frozen=True)
class MetadataRecord:
    logical_id: str
    revision: str
    values: JsonObject


class MetadataConflictError(RuntimeError):
    """The authoritative metadata revision changed before the mutation committed."""

    def __init__(self) -> None:
        super().__init__("metadata mutation rejected")


class DashboardRevisionConflictError(RuntimeError):
    """The authoritative Dashboard workspace changed before its atomic commit."""

    def __init__(self) -> None:
        super().__init__("dashboard revision does not match")


class RevisionedMetadataPort(Protocol):
    async def read(self, query: MetadataQuery) -> tuple[MetadataRecord, ...]: ...

    async def write(self, command: MetadataWrite) -> MetadataRecord: ...

    async def delete(self, command: MetadataDelete) -> None: ...


class InternalMetadataTransport(Protocol):
    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[JsonObject]: ...

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, JsonValue],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject: ...

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> JsonObject: ...


@runtime_checkable
class _CodedTransportError(Protocol):
    code: str


class RevisionedMetadataTransportAdapter:
    """Translate the PocketBase transport shape into the application interface."""

    def __init__(self, transport: InternalMetadataTransport) -> None:
        self._transport = transport

    async def read(self, query: MetadataQuery) -> tuple[MetadataRecord, ...]:
        rows = await self._transport.list_metadata(
            query.namespace,
            keys=list(query.keys) or None,
        )
        return tuple(_record(row) for row in rows)

    async def write(self, command: MetadataWrite) -> MetadataRecord:
        try:
            receipt = await self._transport.upsert_metadata(
                command.namespace,
                record_id=command.logical_id,
                values=command.values,
                expected_revision=command.expected_revision or "",
                idempotency_key=command.idempotency_key,
            )
        except Exception as error:
            raise_metadata_transport_error(error)
        item = receipt.get("item")
        if not isinstance(item, dict):
            raise ValueError("metadata mutation returned an invalid item")
        return _record(_json_object(item))

    async def delete(self, command: MetadataDelete) -> None:
        try:
            await self._transport.delete_metadata(
                command.namespace,
                record_id=command.logical_id,
                expected_revision=command.expected_revision,
                idempotency_key=command.idempotency_key,
            )
        except Exception as error:
            raise_metadata_transport_error(error)


def json_object(value: Mapping[str, object]) -> JsonObject:
    return {key: _json_value(item) for key, item in value.items()}


def _record(row: JsonObject) -> MetadataRecord:
    logical_id = row.get("id")
    revision = row.get("revision")
    if not isinstance(logical_id, str) or not logical_id:
        raise ValueError("metadata record identity is invalid")
    if not isinstance(revision, str) or not revision:
        raise ValueError("metadata record revision is invalid")
    return MetadataRecord(
        logical_id=logical_id,
        revision=revision,
        values={key: value for key, value in row.items() if key not in {"id", "revision"}},
    )


def _json_object(value: object) -> JsonObject:
    if not isinstance(value, dict):
        raise ValueError("metadata value is not a JSON object")
    if not all(isinstance(key, str) for key in value):
        raise ValueError("metadata object keys must be strings")
    return {str(key): _json_value(item) for key, item in value.items()}


def _json_value(value: object) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int, float)):
        return value
    if isinstance(value, list):
        return [_json_value(item) for item in value]
    if isinstance(value, dict):
        return _json_object(value)
    raise ValueError("metadata value is not JSON-compatible")


def raise_metadata_transport_error(error: Exception) -> Never:
    """Raise the application error represented by one metadata transport failure."""

    if isinstance(error, _CodedTransportError) and error.code == "metadata.revision_conflict":
        raise MetadataConflictError() from error
    raise error


__all__ = [
    "DashboardRevisionConflictError",
    "InternalMetadataTransport",
    "JsonObject",
    "JsonValue",
    "MetadataConflictError",
    "MetadataDelete",
    "MetadataQuery",
    "MetadataRecord",
    "MetadataWrite",
    "RevisionedMetadataPort",
    "RevisionedMetadataTransportAdapter",
    "json_object",
    "raise_metadata_transport_error",
]
