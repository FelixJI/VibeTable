"""Stable user display-name to Directus identifier registry."""

from __future__ import annotations

import unicodedata
import uuid
from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass
from typing import Any

REGISTRY_COLLECTION = "vibetable_identifier_map"
SYSTEM_FIELDS = {
    "id",
    "status",
    "sort",
    "date_created",
    "user_created",
    "date_updated",
    "user_updated",
}
_CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


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
    entity_kind: str
    parent_physical_name: str | None
    physical_name: str
    display_name: str
    normalized_name: str
    locale: str = "zh-CN"
    aliases: tuple[str, ...] = ()
    origin: str = "vibetable"
    status: str = "pending"

    def item(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "entity_kind": self.entity_kind,
            "parent_physical_name": self.parent_physical_name,
            "physical_name": self.physical_name,
            "display_name": self.display_name,
            "normalized_name": self.normalized_name,
            "locale": self.locale,
            "aliases": list(self.aliases),
            "origin": self.origin,
            "status": self.status,
        }


class IdentifierRegistry:
    """Persist mappings in Directus and recover mappings for external tables."""

    def __init__(self, transport: Any, *, id_factory: Callable[[], uuid.UUID] = uuid.uuid4) -> None:
        self._transport = transport
        self._id_factory = id_factory

    def allocate_physical(self, kind: str, occupied: Iterable[str] = ()) -> str:
        prefix = "vt_t_" if kind == "collection" else "f_"
        occupied_set = set(occupied)
        for _ in range(32):
            candidate = prefix + stable_suffix(self._id_factory().int & ((1 << 80) - 1))
            if candidate not in occupied_set:
                return candidate
        raise RuntimeError("could not allocate a unique physical identifier")

    def new_record_id(self) -> str:
        return str(self._id_factory())

    async def ensure_collection(self, token: str) -> None:
        payload = await self._transport.request(
            "GET",
            "/collections",
            access_token=token,
            query={"limit": -1, "fields": ["collection"]},
        )
        rows = payload.get("data") if isinstance(payload, dict) else None
        if isinstance(rows, list) and any(
            isinstance(row, dict) and row.get("collection") == REGISTRY_COLLECTION for row in rows
        ):
            return
        await self._transport.request(
            "POST",
            "/collections",
            access_token=token,
            json_body=registry_collection_body(),
            expected_status=(200, 201),
        )

    async def read_all(self, token: str) -> list[IdentifierMapping]:
        await self.ensure_collection(token)
        payload = await self._transport.request(
            "GET",
            f"/items/{REGISTRY_COLLECTION}",
            access_token=token,
            query={
                "limit": -1,
                "fields": [
                    "id",
                    "entity_kind",
                    "parent_physical_name",
                    "physical_name",
                    "display_name",
                    "normalized_name",
                    "locale",
                    "aliases",
                    "origin",
                    "status",
                ],
            },
        )
        rows = payload.get("data") if isinstance(payload, dict) else None
        result: list[IdentifierMapping] = []
        if not isinstance(rows, list):
            return result
        for row in rows:
            if not isinstance(row, Mapping):
                continue
            try:
                result.append(
                    IdentifierMapping(
                        id=str(row["id"]),
                        entity_kind=str(row["entity_kind"]),
                        parent_physical_name=_optional_string(row.get("parent_physical_name")),
                        physical_name=str(row["physical_name"]),
                        display_name=str(row["display_name"]),
                        normalized_name=str(
                            row.get("normalized_name")
                            or normalize_display_name(str(row["display_name"]))
                        ),
                        locale=str(row.get("locale") or "zh-CN"),
                        aliases=tuple(
                            str(item)
                            for item in (row.get("aliases") or [])
                            if isinstance(item, str)
                        ),
                        origin=str(row.get("origin") or "directus"),
                        status=str(row.get("status") or "active"),
                    )
                )
            except (KeyError, TypeError, ValueError):
                continue
        return result

    async def create(self, token: str, mappings: list[IdentifierMapping]) -> None:
        if not mappings:
            return
        await self.ensure_collection(token)
        await self._transport.request(
            "POST",
            f"/items/{REGISTRY_COLLECTION}",
            access_token=token,
            json_body=[mapping.item() for mapping in mappings],
            expected_status=(200, 201),
        )

    async def set_status(
        self, token: str, mappings: Iterable[IdentifierMapping], status: str
    ) -> None:
        for mapping in mappings:
            await self._transport.request(
                "PATCH",
                f"/items/{REGISTRY_COLLECTION}/{mapping.id}",
                access_token=token,
                json_body={"status": status},
                expected_status=(200, 201),
            )

    async def update_display(
        self, token: str, mapping: IdentifierMapping, display_name: str
    ) -> None:
        aliases = list(dict.fromkeys([*mapping.aliases, mapping.display_name]))
        await self._transport.request(
            "PATCH",
            f"/items/{REGISTRY_COLLECTION}/{mapping.id}",
            access_token=token,
            json_body={
                "display_name": display_name,
                "normalized_name": normalize_display_name(display_name),
                "aliases": aliases,
            },
            expected_status=(200, 201),
        )

    async def update_aliases(
        self, token: str, mapping: IdentifierMapping, aliases: Iterable[str]
    ) -> None:
        await self._transport.request(
            "PATCH",
            f"/items/{REGISTRY_COLLECTION}/{mapping.id}",
            access_token=token,
            json_body={"aliases": list(aliases)},
            expected_status=(200, 201),
        )


def translated_title(meta: Any, fallback: str) -> str:
    if isinstance(meta, Mapping):
        translations = meta.get("translations")
        if isinstance(translations, list):
            preferred = sorted(
                (item for item in translations if isinstance(item, Mapping)),
                key=lambda item: 0 if item.get("language") == "zh-CN" else 1,
            )
            for item in preferred:
                value = item.get("translation")
                if isinstance(value, str) and value.strip():
                    return value.strip()
    return fallback


def translation(display_name: str, locale: str = "zh-CN") -> list[dict[str, str]]:
    return [{"language": locale, "translation": display_name}]


def registry_collection_body() -> dict[str, Any]:
    def field(name: str, type_name: str, *, required: bool = False) -> dict[str, Any]:
        return {
            "field": name,
            "type": type_name,
            "meta": {"field": name, "required": required},
            "schema": {"name": name, "data_type": type_name, "is_nullable": not required},
        }

    fields = [
        {
            "field": "id",
            "type": "uuid",
            "meta": {"required": True, "readonly": True},
            "schema": {
                "name": "id",
                "data_type": "uuid",
                "is_primary_key": True,
                "is_nullable": False,
            },
        },
        field("entity_kind", "string", required=True),
        field("parent_physical_name", "string"),
        field("physical_name", "string", required=True),
        field("display_name", "string", required=True),
        field("normalized_name", "string", required=True),
        field("locale", "string", required=True),
        field("aliases", "json"),
        field("origin", "string", required=True),
        {
            "field": "status",
            "type": "string",
            "meta": {"field": "status", "required": True},
            "schema": {
                "name": "status",
                "data_type": "string",
                "is_nullable": False,
                "default_value": "pending",
            },
        },
        field("sort", "integer"),
        {
            "field": "date_created",
            "type": "timestamp",
            "meta": {"field": "date_created", "readonly": True, "special": ["date-created"]},
            "schema": {"name": "date_created", "data_type": "timestamp", "is_nullable": True},
        },
        {
            "field": "user_created",
            "type": "uuid",
            "meta": {"field": "user_created", "readonly": True, "special": ["user-created"]},
            "schema": {"name": "user_created", "data_type": "uuid", "is_nullable": True},
        },
        {
            "field": "date_updated",
            "type": "timestamp",
            "meta": {"field": "date_updated", "readonly": True, "special": ["date-updated"]},
            "schema": {"name": "date_updated", "data_type": "timestamp", "is_nullable": True},
        },
        {
            "field": "user_updated",
            "type": "uuid",
            "meta": {"field": "user_updated", "readonly": True, "special": ["user-updated"]},
            "schema": {"name": "user_updated", "data_type": "uuid", "is_nullable": True},
        },
    ]
    return {
        "collection": REGISTRY_COLLECTION,
        "schema": {"name": REGISTRY_COLLECTION},
        "meta": {"collection": REGISTRY_COLLECTION, "hidden": True, "accountability": "all"},
        "fields": fields,
    }


def _optional_string(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


__all__ = [
    "REGISTRY_COLLECTION",
    "SYSTEM_FIELDS",
    "IdentifierMapping",
    "IdentifierRegistry",
    "normalize_display_name",
    "registry_collection_body",
    "stable_suffix",
    "translated_title",
    "translation",
]
