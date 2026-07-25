"""Stable display-name to product identifier registry."""

from __future__ import annotations

import builtins
import unicodedata
import uuid
from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass
from typing import Any, Literal, Protocol, cast

IdentifierEntityKind = Literal["collection", "field"]
IdentifierOrigin = Literal["vibetable", "pocketbase"]
IdentifierStatus = Literal["pending", "active"]

# Kept as a logical compatibility export until TableAdmin moves to
# SchemaCatalog. It is not a physical collection name.
REGISTRY_COLLECTION = "identifier_mappings"
SYSTEM_FIELDS = {"id", "created", "updated"}
_CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


class IdentifierMetadataPort(Protocol):
    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]: ...

    async def upsert_metadata(
        self,
        namespace: str,
        *,
        record_id: str | None,
        values: Mapping[str, Any],
        expected_revision: str | None,
        idempotency_key: str,
    ) -> dict[str, Any]: ...

    async def delete_metadata(
        self,
        namespace: str,
        *,
        record_id: str,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> dict[str, Any]: ...


def normalize_display_name(value: str) -> str:
    return unicodedata.normalize("NFKC", value).strip().casefold()


def stable_suffix(value: int, length: int = 16) -> str:
    chars: list[str] = []
    for _ in range(length):
        chars.append(_CROCKFORD[value & 31])
        value >>= 5
    return "".join(reversed(chars))


@dataclass(frozen=True, slots=True)
class IdentifierMapping:
    id: str
    entity_kind: IdentifierEntityKind
    parent_physical_name: str | None
    physical_name: str
    display_name: str
    normalized_name: str
    locale: str = "zh-CN"
    aliases: tuple[str, ...] = ()
    origin: IdentifierOrigin = "vibetable"
    status: IdentifierStatus = "pending"
    revision: str | None = None

    def item(self) -> dict[str, Any]:
        _entity_kind(self.entity_kind)
        _origin(self.origin)
        _status(self.status)
        return {
            "id": self.id,
            "entityKind": self.entity_kind,
            "parentPhysicalName": self.parent_physical_name,
            "physicalName": self.physical_name,
            "displayName": self.display_name,
            "normalizedName": self.normalized_name,
            "locale": self.locale,
            "aliases": list(self.aliases),
            "origin": self.origin,
            "status": self.status,
        }


class IdentifierRegistry:
    """Persist mappings through the internal-metadata product port."""

    def __init__(
        self,
        metadata_port: IdentifierMetadataPort,
        *,
        id_factory: Callable[[], uuid.UUID] = uuid.uuid4,
    ) -> None:
        self._metadata = metadata_port
        self._id_factory = id_factory

    def allocate_physical(
        self,
        kind: IdentifierEntityKind,
        occupied: Iterable[str] = (),
    ) -> str:
        _entity_kind(kind)
        prefix = "vt_t_" if kind == "collection" else "f_"
        occupied_set = set(occupied)
        for _ in range(32):
            candidate = prefix + stable_suffix(self._id_factory().int & ((1 << 80) - 1))
            if candidate not in occupied_set:
                return candidate
        raise RuntimeError("could not allocate a unique physical identifier")

    def new_record_id(self) -> str:
        return str(self._id_factory())

    async def read_all(self) -> list[IdentifierMapping]:
        rows = await self._metadata.list_metadata("identifier_mappings")
        result: list[IdentifierMapping] = []
        for row in rows:
            try:
                display_name = _required_text(row, "displayName")
                aliases = row.get("aliases", [])
                result.append(
                    IdentifierMapping(
                        id=_required_text(row, "id"),
                        entity_kind=_entity_kind(row.get("entityKind")),
                        parent_physical_name=_optional_text(row.get("parentPhysicalName")),
                        physical_name=_required_text(row, "physicalName"),
                        display_name=display_name,
                        normalized_name=_optional_text(row.get("normalizedName"))
                        or normalize_display_name(display_name),
                        locale=_optional_text(row.get("locale")) or "zh-CN",
                        aliases=tuple(
                            value for value in aliases if isinstance(value, str) and value
                        )
                        if isinstance(aliases, list)
                        else (),
                        origin=_origin(row.get("origin")),
                        status=_status(row.get("status")),
                        revision=_optional_text(row.get("revision")),
                    )
                )
            except (KeyError, TypeError, ValueError):
                continue
        return result

    async def create(self, mappings: list[IdentifierMapping]) -> None:
        for mapping in mappings:
            await self._write(
                mapping,
                mapping.item(),
                operation="create",
            )

    async def set_status(
        self,
        mappings: Iterable[IdentifierMapping],
        status: IdentifierStatus,
    ) -> None:
        _status(status)
        for mapping in mappings:
            await self._write(
                mapping,
                {"status": status},
                operation=f"status:{status}",
            )

    async def update_display(
        self,
        mapping: IdentifierMapping,
        display_name: str,
    ) -> None:
        aliases = list(dict.fromkeys([*mapping.aliases, mapping.display_name]))
        await self._write(
            mapping,
            {
                "displayName": display_name,
                "normalizedName": normalize_display_name(display_name),
                "aliases": aliases,
            },
            operation="display",
        )

    async def update_aliases(
        self,
        mapping: IdentifierMapping,
        aliases: Iterable[str],
    ) -> None:
        await self._write(
            mapping,
            {"aliases": list(aliases)},
            operation="aliases",
        )

    async def _write(
        self,
        mapping: IdentifierMapping,
        values: Mapping[str, Any],
        *,
        operation: str,
    ) -> None:
        await self._metadata.upsert_metadata(
            "identifier_mappings",
            record_id=mapping.id,
            values=values,
            expected_revision=mapping.revision,
            idempotency_key=f"identifier:{operation}:{mapping.id}",
        )


class IdentifierManagementError(Exception):
    """Stable product error for user-facing identifier management."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class IdentifierSchemaPort(Protocol):
    async def list_tables(self) -> dict[str, Any]: ...

    async def describe_table(self, table_id: str) -> dict[str, Any]: ...


class IdentifierManagementService:
    """Manage display aliases against the authoritative product schema."""

    def __init__(
        self,
        *,
        registry: IdentifierRegistry,
        schema_port: IdentifierSchemaPort,
    ) -> None:
        self._registry = registry
        self._schema = schema_port

    async def list(self, params: Any) -> Any:
        from backend.contracts.table_admin import (
            IdentifierMappingEntry,
            IdentifierMappingsResult,
        )

        mappings = await self._registry.read_all()
        needle = normalize_display_name(getattr(params, "search", None) or "")
        if needle:
            mappings = [
                item
                for item in mappings
                if needle in normalize_display_name(item.display_name)
                or needle in item.physical_name.casefold()
                or any(needle in normalize_display_name(alias) for alias in item.aliases)
            ]
        ordered = sorted(
            mappings,
            key=lambda item: (
                0 if item.entity_kind == "collection" else 1,
                item.parent_physical_name or "",
                normalize_display_name(item.display_name),
                item.physical_name,
            ),
        )
        return IdentifierMappingsResult(
            mappings=[
                IdentifierMappingEntry(
                    id=item.id,
                    entity_kind=item.entity_kind,
                    parent_physical_name=item.parent_physical_name,
                    physical_name=item.physical_name,
                    display_name=item.display_name,
                    locale=item.locale,
                    aliases=list(item.aliases),
                    origin=item.origin,
                    status=item.status,
                )
                for item in ordered
            ]
        )

    async def update_aliases(self, params: Any) -> Any:
        mappings = await self._registry.read_all()
        target = self._require_mapping(mappings, params.mapping_id)
        aliases = self._validate_aliases(target, list(params.aliases), mappings)
        await self._registry.update_aliases(target, aliases)
        return await self._list_all()

    async def reconcile(self, _params: Any) -> Any:
        mappings = await self._registry.read_all()
        by_identity = {
            (item.entity_kind, item.parent_physical_name, item.physical_name): item
            for item in mappings
        }
        catalog = await self._schema.list_tables()
        raw_tables = catalog.get("tables")
        if not isinstance(raw_tables, list):
            raise IdentifierManagementError(
                "product schema catalog is invalid",
                code="identifier_schema_invalid",
            )
        adopted: list[IdentifierMapping] = []
        reactivate: list[IdentifierMapping] = []
        for item in raw_tables:
            if not isinstance(item, Mapping):
                continue
            table_id = item.get("tableId")
            if not isinstance(table_id, str) or not table_id or table_id.startswith("vibetable_"):
                continue
            definition = await self._schema.describe_table(table_id)
            table_display = _optional_text(definition.get("displayName")) or table_id
            table_identity: tuple[IdentifierEntityKind, str | None, str] = (
                "collection",
                None,
                table_id,
            )
            table_mapping = by_identity.get(table_identity)
            if table_mapping is None:
                adopted.append(
                    self._mapping(
                        kind="collection",
                        parent=None,
                        physical=table_id,
                        display=table_display,
                    )
                )
            elif table_mapping.status != "active":
                reactivate.append(table_mapping)
            raw_fields = definition.get("fields")
            if not isinstance(raw_fields, list):
                raise IdentifierManagementError(
                    "product table schema is invalid",
                    code="identifier_schema_invalid",
                )
            for raw_field in raw_fields:
                if not isinstance(raw_field, Mapping) or raw_field.get("kind") == "system":
                    continue
                physical = raw_field.get("physicalName")
                if not isinstance(physical, str) or not physical:
                    continue
                identity: tuple[IdentifierEntityKind, str | None, str] = (
                    "field",
                    table_id,
                    physical,
                )
                field_mapping = by_identity.get(identity)
                if field_mapping is None:
                    display = _optional_text(raw_field.get("displayName")) or physical
                    adopted.append(
                        self._mapping(
                            kind="field",
                            parent=table_id,
                            physical=physical,
                            display=display,
                        )
                    )
                elif field_mapping.status != "active":
                    reactivate.append(field_mapping)
        if adopted:
            await self._registry.create(adopted)
        if reactivate:
            await self._registry.set_status(reactivate, "active")
        return await self._list_all()

    async def _list_all(self) -> Any:
        from backend.contracts.table_admin import ListIdentifierMappingsParams

        return await self.list(ListIdentifierMappingsParams())

    @staticmethod
    def _require_mapping(
        mappings: builtins.list[IdentifierMapping],
        mapping_id: str,
    ) -> IdentifierMapping:
        target = next((item for item in mappings if item.id == mapping_id), None)
        if target is None:
            raise IdentifierManagementError(
                "identifier mapping no longer exists",
                code="mapping_not_found",
            )
        return target

    @staticmethod
    def _validate_aliases(
        target: IdentifierMapping,
        values: builtins.list[str],
        mappings: builtins.list[IdentifierMapping],
    ) -> builtins.list[str]:
        aliases: builtins.list[str] = []
        seen: set[str] = set()
        for raw in values:
            value = raw.strip()
            if not value or len(value) > 128:
                raise IdentifierManagementError(
                    "alias must contain 1 to 128 characters",
                    code="alias_invalid",
                )
            if any(unicodedata.category(char) in {"Cc", "Cs"} for char in value):
                raise IdentifierManagementError(
                    "alias must not contain control characters",
                    code="alias_invalid",
                )
            normalized = normalize_display_name(value)
            if normalized == target.normalized_name or normalized in seen:
                continue
            conflict = next(
                (
                    item
                    for item in mappings
                    if item.id != target.id
                    and item.parent_physical_name == target.parent_physical_name
                    and (
                        item.normalized_name == normalized
                        or any(
                            normalize_display_name(alias) == normalized for alias in item.aliases
                        )
                    )
                ),
                None,
            )
            if conflict is not None:
                raise IdentifierManagementError(
                    f"alias {value!r} is already used",
                    code="alias_duplicate",
                )
            seen.add(normalized)
            aliases.append(value)
        return aliases

    def _mapping(
        self,
        *,
        kind: IdentifierEntityKind,
        parent: str | None,
        physical: str,
        display: str,
    ) -> IdentifierMapping:
        return IdentifierMapping(
            id=self._registry.new_record_id(),
            entity_kind=kind,
            parent_physical_name=parent,
            physical_name=physical,
            display_name=display,
            normalized_name=normalize_display_name(display),
            origin="pocketbase",
            status="active",
        )


def _entity_kind(value: object) -> IdentifierEntityKind:
    if value in {"collection", "field"}:
        return cast(IdentifierEntityKind, value)
    raise ValueError(f"unsupported identifier entity kind: {value!r}")


def _origin(value: object) -> IdentifierOrigin:
    if value in {"vibetable", "pocketbase"}:
        return cast(IdentifierOrigin, value)
    raise ValueError(f"unsupported identifier origin: {value!r}")


def _status(value: object) -> IdentifierStatus:
    if value in {"pending", "active"}:
        return cast(IdentifierStatus, value)
    raise ValueError(f"unsupported identifier status: {value!r}")


def translated_title(meta: Any, fallback: str) -> str:
    """Read a normalized display name during the TableAdmin transition."""
    if isinstance(meta, Mapping):
        for key in ("displayName", "display_name"):
            value = meta.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    return fallback


def translation(display_name: str, locale: str = "zh-CN") -> list[dict[str, str]]:
    """Compatibility projection retained until the old TableAdmin is removed."""
    return [{"language": locale, "translation": display_name}]


def _required_text(row: Mapping[str, Any], name: str) -> str:
    value = row[name]
    if not isinstance(value, str) or not value:
        raise ValueError(f"{name} must be non-empty text")
    return value


def _optional_text(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


__all__ = [
    "REGISTRY_COLLECTION",
    "SYSTEM_FIELDS",
    "IdentifierEntityKind",
    "IdentifierManagementError",
    "IdentifierManagementService",
    "IdentifierMapping",
    "IdentifierOrigin",
    "IdentifierRegistry",
    "IdentifierStatus",
    "normalize_display_name",
    "stable_suffix",
    "translated_title",
    "translation",
]
