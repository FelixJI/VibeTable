from __future__ import annotations

import re
import unicodedata
import uuid
from collections.abc import Callable, Mapping
from typing import Any, Protocol

from backend.adapters.directus.profile import CollectionProfile
from backend.application.identifier_mapping_service import (
    SYSTEM_FIELDS,
    IdentifierEntityKind,
    IdentifierMapping,
    IdentifierOrigin,
    IdentifierRegistry,
    IdentifierStatus,
    normalize_display_name,
    translated_title,
    translation,
)
from backend.contracts.table_admin import (
    CreateTableParams,
    CreateTableResult,
    DeleteIdentifierMappingParams,
    DeleteTableParams,
    DeleteTableResult,
    FieldDefinition,
    IdentifierMappingEntry,
    IdentifierMappingsResult,
    ImportIdentifierMappingsParams,
    ListIdentifierMappingsParams,
    PurgeIdentifierMappingsParams,
    ReconcileIdentifierMappingsParams,
    UpdateIdentifierAliasesParams,
)


class _AccessTokenProvider(Protocol):
    """Anything that yields the current user's Directus access token.

    ``DirectusAuthBroker`` satisfies this protocol (its ``access_token`` is an
    async method returning ``str``).
    """

    async def access_token(self) -> str: ...


# Directus 类型映射（VibeTable 字段类型 → Directus schema data_type）
_TYPE_MAP: dict[str, str] = {
    "string": "string",
    "text": "text",
    "integer": "integer",
    "bigInteger": "bigInteger",
    "float": "float",
    "decimal": "decimal",
    "boolean": "boolean",
    "date": "date",
    "dateTime": "dateTime",
    "timestamp": "timestamp",
    "time": "time",
    "json": "json",
    "csv": "csv",
    "uuid": "uuid",
    "hash": "hash",
    "binary": "binary",
}

_SYSTEM_FIELDS = ["status", "sort", "date_created", "user_created", "date_updated", "user_updated"]
# The whole ``vibetable_`` namespace is reserved for system/bootstrap tables
# (vibetable_workspaces, vibetable_documents, vibetable_settings, ...). Matching
# the full prefix (rather than enumerating each singular substring like
# ``vibetable_document``) also covers system tables added later without silently
# letting a user shadow them. ``directus_`` covers all Directus internal tables.
_RESERVED_PREFIXES = ("directus_", "vibetable_")
_PHYSICAL_IDENTIFIER = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,127}$")


class TableAdminError(Exception):
    """Table administration error."""

    def __init__(self, message: str, *, code: str = "table_admin_error") -> None:
        super().__init__(message)
        self.code = code

    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code, "message": str(self)}


class TableAdminService:
    """Create/delete Directus collections at runtime (admin token).

    For a desktop single-user app where the current user IS the admin (the
    local-Directus single-machine mode bootstraps an admin account), the
    service uses the current user's access token fetched per-call through the
    auth broker (mirroring ``DirectusClient``). A user with an admin/manager
    role can create/delete collections.
    """

    def __init__(
        self,
        *,
        transport: Any,
        auth: _AccessTokenProvider,
        profiles: dict[str, CollectionProfile],
        id_factory: Callable[[], uuid.UUID] = uuid.uuid4,
    ) -> None:
        self._transport = transport
        self._auth = auth
        # 运行时 manifest 的 by_collection 视图（可变 dict）。
        # 真实 CapabilityManifest.by_collection 是只读计算属性（frozen 模型），
        # DirectusService 传入了它已维护的可变 ``_profiles`` 快照。
        # create_table/delete_table 同步增删该快照（写入真正的 CollectionProfile），
        # 使建表后当前会话立即可用；重启时 manifest 从 Directus 重读。
        self._profiles: dict[str, CollectionProfile] = profiles
        self._registry = IdentifierRegistry(transport, id_factory=id_factory)
        self._display_names: dict[str, str] = {}

    async def create_table(self, params: CreateTableParams) -> CreateTableResult:
        display_name = params.name
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        self._validate_display_names(display_name, params.fields, mappings)
        collection = self._registry.allocate_physical(
            "collection", [*self._profiles, *(item.physical_name for item in mappings)]
        )
        field_physical_names: list[str] = []
        occupied = {item.physical_name for item in mappings if item.entity_kind == "field"}
        for _ in params.fields:
            physical = self._registry.allocate_physical("field", occupied)
            occupied.add(physical)
            field_physical_names.append(physical)
        physical_fields = [
            FieldDefinition(key=physical, type=source.type)
            for source, physical in zip(params.fields, field_physical_names, strict=True)
        ]
        # Build the runtime profile BEFORE touching Directus. If profile
        # construction fails (e.g. the identifier rules in CollectionProfile
        # reject a name the contract layer somehow let through), we raise here
        # and never POST /collections — otherwise Directus would create the
        # collection and we would leave an orphan behind when this call fails.
        # ValidationError is wrapped as TableAdminError so the dispatcher maps
        # it to the registered table_admin code (-32110) instead of falling
        # through to the opaque -32603 "Internal error" bucket.
        try:
            profile = self._profile_for(collection, physical_fields)
        except TableAdminError:
            raise
        except Exception as exc:
            raise TableAdminError(
                f"cannot build capability profile for {display_name!r}: {exc}",
                code="profile_build_failed",
            ) from exc
        collection_mapping = self._mapping(
            kind="collection", parent=None, physical=collection, display=display_name
        )
        field_mappings = [
            self._mapping(kind="field", parent=collection, physical=physical, display=source.key)
            for source, physical in zip(params.fields, field_physical_names, strict=True)
        ]
        reserved = [collection_mapping, *field_mappings]
        await self._registry.create(token, reserved)
        try:
            collection_body = self._build_collection_body(
                collection, display_name, physical_fields, params.fields
            )
            await self._transport.request(
                "POST",
                "/collections",
                access_token=token,
                json_body=collection_body,
                expected_status=(200, 201),
            )
        except Exception:
            await self._registry.set_status(token, reserved, "orphaned")
            raise
        await self._registry.set_status(token, reserved, "active")
        # 同步更新运行时 manifest 白名单：写入真正的 CollectionProfile，
        # 使建表后当前会话内即可对该表执行 read/create/update（_profiles 查询生效）。
        self._profiles[collection] = profile
        self._display_names[collection] = display_name
        return CreateTableResult(
            collection=collection,
            display_name=display_name,
            primary_key=profile.primary_key,
            fields=list(profile.fields),
            field_display_names=dict(
                zip(field_physical_names, (field.key for field in params.fields), strict=True)
            ),
        )

    async def delete_table(self, params: DeleteTableParams) -> DeleteTableResult:
        name = params.name
        self._validate_name(name)
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE",
            f"/collections/{name}",
            access_token=token,
            expected_status=(200, 204),
        )
        self._profiles.pop(name, None)
        self._display_names.pop(name, None)
        mappings = await self._registry.read_all(token)
        await self._registry.set_status(
            token,
            (
                mapping
                for mapping in mappings
                if mapping.physical_name == name or mapping.parent_physical_name == name
            ),
            "deleted",
        )
        return DeleteTableResult(collection=name, deleted=True)

    @property
    def display_names(self) -> dict[str, str]:
        return dict(self._display_names)

    async def reconcile_identifiers(self) -> dict[str, str]:
        """Idempotently adopt external Directus collections and restore profiles."""
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        payload = await self._transport.request(
            "GET",
            "/collections",
            access_token=token,
            query={"limit": -1, "fields": ["collection", "meta.translations", "meta.hidden"]},
        )
        raw_collections = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(raw_collections, list):
            self._display_names = {
                item.physical_name: item.display_name
                for item in mappings
                if item.entity_kind == "collection" and item.status == "active"
            }
            return self.display_names

        collection_maps = {
            item.physical_name: item for item in mappings if item.entity_kind == "collection"
        }
        field_maps = {
            (item.parent_physical_name, item.physical_name): item
            for item in mappings
            if item.entity_kind == "field"
        }
        normalized_by_parent = {
            (item.parent_physical_name, item.normalized_name)
            for item in mappings
            if item.status not in {"deleted", "orphaned"}
        }
        adopted: list[IdentifierMapping] = []
        reactivated: list[IdentifierMapping] = []
        present: set[str] = set()
        present_fields: set[tuple[str, str]] = set()
        display_names: dict[str, str] = {}
        for raw in raw_collections:
            if not isinstance(raw, Mapping):
                continue
            collection = raw.get("collection")
            raw_meta = raw.get("meta")
            if (
                not isinstance(collection, str)
                or collection.startswith(_RESERVED_PREFIXES)
                or (isinstance(raw_meta, Mapping) and raw_meta.get("hidden") is True)
            ):
                continue
            present.add(collection)
            mapped = collection_maps.get(collection)
            display = mapped.display_name if mapped else translated_title(raw_meta, collection)
            if mapped is None:
                display = self._unique_adopted_name(display, None, normalized_by_parent)
                mapped = self._mapping(
                    kind="collection",
                    parent=None,
                    physical=collection,
                    display=display,
                    origin="directus",
                    status="active",
                )
                adopted.append(mapped)
                normalized_by_parent.add((None, mapped.normalized_name))
            else:
                if mapped.status != "active":
                    reactivated.append(mapped)
                    normalized_by_parent.add((None, mapped.normalized_name))
                projected = translated_title(raw_meta, mapped.display_name)
                projected_key = normalize_display_name(projected)
                if projected != mapped.display_name and (
                    projected_key == mapped.normalized_name
                    or (None, projected_key) not in normalized_by_parent
                ):
                    await self._registry.update_display(token, mapped, projected)
                    normalized_by_parent.discard((None, mapped.normalized_name))
                    normalized_by_parent.add((None, projected_key))
                    display = projected
            display_names[collection] = display
            fields_payload = await self._transport.request(
                "GET",
                f"/fields/{collection}",
                access_token=token,
            )
            fields = fields_payload.get("data") if isinstance(fields_payload, dict) else None
            if not isinstance(fields, list):
                continue
            physical_fields: list[str] = []
            for raw_field in fields:
                if not isinstance(raw_field, Mapping):
                    continue
                physical = raw_field.get("field")
                if not isinstance(physical, str):
                    continue
                physical_fields.append(physical)
                if physical in SYSTEM_FIELDS:
                    continue
                present_fields.add((collection, physical))
                existing_field = field_maps.get((collection, physical))
                if existing_field is not None:
                    if existing_field.status != "active":
                        reactivated.append(existing_field)
                        normalized_by_parent.add((collection, existing_field.normalized_name))
                    projected = translated_title(raw_field.get("meta"), existing_field.display_name)
                    projected_key = normalize_display_name(projected)
                    if projected != existing_field.display_name and (
                        projected_key == existing_field.normalized_name
                        or (collection, projected_key) not in normalized_by_parent
                    ):
                        await self._registry.update_display(token, existing_field, projected)
                        normalized_by_parent.discard((collection, existing_field.normalized_name))
                        normalized_by_parent.add((collection, projected_key))
                    continue
                field_display = translated_title(raw_field.get("meta"), physical)
                field_display = self._unique_adopted_name(
                    field_display, collection, normalized_by_parent
                )
                adopted_field = self._mapping(
                    kind="field",
                    parent=collection,
                    physical=physical,
                    display=field_display,
                    origin="directus",
                    status="active",
                )
                adopted.append(adopted_field)
                normalized_by_parent.add((collection, adopted_field.normalized_name))
            if "id" in physical_fields:
                self._profiles[collection] = self._profile_from_existing(
                    collection, physical_fields
                )

        if adopted:
            await self._registry.create(token, adopted)
        if reactivated:
            await self._registry.set_status(token, reactivated, "active")
        orphaned_collections = [
            item
            for physical, item in collection_maps.items()
            if item.status == "active" and physical not in present
        ]
        orphaned_fields = [
            item
            for (parent, physical), item in field_maps.items()
            if item.status == "active"
            and (parent not in present or (parent or "", physical) not in present_fields)
        ]
        orphaned = [*orphaned_collections, *orphaned_fields]
        if orphaned:
            await self._registry.set_status(token, orphaned, "orphaned")

        # list_collections is the source of truth after a Directus Studio
        # schema edit. Remove profiles for externally deleted user tables so
        # DirectusService.list_collections cannot return stale sidebar entries.
        for physical, profile in list(self._profiles.items()):
            if (
                not profile.hidden
                and not physical.startswith(_RESERVED_PREFIXES)
                and physical not in present
            ):
                self._profiles.pop(physical, None)
        self._display_names = display_names
        return self.display_names

    async def list_identifier_mappings(
        self, params: ListIdentifierMappingsParams
    ) -> IdentifierMappingsResult:
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        needle = normalize_display_name(params.search or "")
        if needle:
            mappings = [
                item
                for item in mappings
                if needle in normalize_display_name(item.display_name)
                or needle in item.physical_name.casefold()
                or any(needle in normalize_display_name(alias) for alias in item.aliases)
            ]
        return self._mapping_result(mappings)

    async def update_identifier_aliases(
        self, params: UpdateIdentifierAliasesParams
    ) -> IdentifierMappingsResult:
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        target = next((item for item in mappings if item.id == params.mapping_id), None)
        if target is None:
            raise TableAdminError("identifier mapping no longer exists", code="mapping_not_found")
        aliases = self._validate_aliases(target, params.aliases, mappings)
        await self._registry.update_aliases(token, target, aliases)
        return await self.list_identifier_mappings(ListIdentifierMappingsParams())

    async def import_identifier_mappings(
        self, params: ImportIdentifierMappingsParams
    ) -> IdentifierMappingsResult:
        """Merge portable aliases into matching physical identities.

        Import never creates or renames a physical identifier. Unknown rows are
        ignored so an export from another Directus instance cannot mutate this
        schema accidentally.
        """
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        by_identity = {
            (item.entity_kind, item.parent_physical_name, item.physical_name): item
            for item in mappings
        }
        for incoming in params.mappings:
            target = by_identity.get(
                (
                    incoming.entity_kind,
                    incoming.parent_physical_name,
                    incoming.physical_name,
                )
            )
            if target is None:
                continue
            candidates = [*target.aliases, *incoming.aliases]
            if normalize_display_name(incoming.display_name) != target.normalized_name:
                candidates.append(incoming.display_name)
            aliases = self._validate_aliases(target, candidates, mappings)
            await self._registry.update_aliases(token, target, aliases)
        return await self.list_identifier_mappings(ListIdentifierMappingsParams())

    async def reconcile_identifier_mappings(
        self, _params: ReconcileIdentifierMappingsParams
    ) -> IdentifierMappingsResult:
        await self.reconcile_identifiers()
        return await self.list_identifier_mappings(ListIdentifierMappingsParams())

    async def delete_identifier_mapping(
        self, params: DeleteIdentifierMappingParams
    ) -> IdentifierMappingsResult:
        """Permanently remove one registry row.

        Only ``orphaned`` or ``deleted`` mappings are removable — ``active`` /
        ``pending`` rows stay coupled to the physical Directus collection and
        must leave through ``delete_table`` instead.
        """
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        target = next((item for item in mappings if item.id == params.mapping_id), None)
        if target is None:
            raise TableAdminError("identifier mapping no longer exists", code="mapping_not_found")
        if target.status not in {"orphaned", "deleted"}:
            raise TableAdminError(
                "only orphaned or deleted mappings can be removed",
                code="mapping_not_removable",
            )
        await self._registry.delete(token, target)
        return await self.list_identifier_mappings(ListIdentifierMappingsParams())

    async def purge_identifier_mappings(
        self, _params: PurgeIdentifierMappingsParams
    ) -> IdentifierMappingsResult:
        """Permanently remove every ``orphaned`` / ``deleted`` registry row."""
        token = await self._auth.access_token()
        mappings = await self._registry.read_all(token)
        removable = [mapping for mapping in mappings if mapping.status in {"orphaned", "deleted"}]
        if removable:
            await self._registry.delete_many(token, removable)
        return await self.list_identifier_mappings(ListIdentifierMappingsParams())

    @staticmethod
    def _mapping_result(mappings: list[IdentifierMapping]) -> IdentifierMappingsResult:
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

    def _validate_aliases(
        self,
        target: IdentifierMapping,
        values: list[str],
        mappings: list[IdentifierMapping],
    ) -> list[str]:
        aliases: list[str] = []
        seen: set[str] = set()
        for raw in values:
            value = raw.strip()
            if not value or len(value) > 128:
                raise TableAdminError(
                    "alias must contain 1 to 128 characters", code="alias_invalid"
                )
            if any(unicodedata.category(char) in {"Cc", "Cs"} for char in value):
                raise TableAdminError(
                    "alias must not contain control characters", code="alias_invalid"
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
                    and item.status not in {"deleted", "orphaned"}
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
                raise TableAdminError(
                    f"alias {value!r} is already used by {conflict.display_name!r}",
                    code="alias_duplicate",
                )
            seen.add(normalized)
            aliases.append(value)
        return aliases

    def _validate_name(self, name: str) -> None:
        if not _PHYSICAL_IDENTIFIER.fullmatch(name):
            raise TableAdminError(
                f"collection name {name!r} is not a valid physical identifier",
                code="table_name_invalid",
            )
        if name.startswith(_RESERVED_PREFIXES):
            raise TableAdminError(
                f"collection name {name!r} is reserved or conflicts with system tables",
                code="table_name_reserved",
            )

    def _validate_display_names(
        self,
        table_name: str,
        fields: list[FieldDefinition],
        mappings: list[IdentifierMapping],
    ) -> None:
        normalized_table = normalize_display_name(table_name)
        if any(
            item.entity_kind == "collection"
            and item.normalized_name == normalized_table
            and item.status not in {"deleted", "orphaned"}
            for item in mappings
        ):
            raise TableAdminError(
                f"table display name {table_name!r} already exists", code="table_name_duplicate"
            )
        normalized_fields = [normalize_display_name(field.key) for field in fields]
        if len(normalized_fields) != len(set(normalized_fields)):
            raise TableAdminError(
                "field display names must be unique within a table", code="field_name_duplicate"
            )

    def _mapping(
        self,
        *,
        kind: IdentifierEntityKind,
        parent: str | None,
        physical: str,
        display: str,
        origin: IdentifierOrigin = "vibetable",
        status: IdentifierStatus = "pending",
    ) -> IdentifierMapping:
        return IdentifierMapping(
            id=self._registry.new_record_id(),
            entity_kind=kind,
            parent_physical_name=parent,
            physical_name=physical,
            display_name=display,
            normalized_name=normalize_display_name(display),
            origin=origin,
            status=status,
        )

    @staticmethod
    def _profile_for(name: str, fields: list[FieldDefinition]) -> CollectionProfile:
        """Build the runtime capability profile for a freshly created table.

        Mirrors the schema the BFF just POSTed (id PK + archive/audit system
        fields + the user-declared fields) so the manifest stays consistent and
        the table is immediately usable in the current session.
        """
        declared = [f.key for f in fields]
        full_fields = ["id", *_SYSTEM_FIELDS, *declared]
        return CollectionProfile(
            collection=name,
            primary_key="id",
            fields=full_fields,
            create_fields=["id", "status", "sort", *declared],
            update_fields=["status", "sort", *declared],
            archive_field="status",
            archive_value="archived",
            restore_value="active",
            date_updated_field="date_updated",
            allow_permanent_delete=False,
            allow_revision_history=True,
            allow_revision_revert=True,
        )

    @staticmethod
    def _profile_from_existing(name: str, fields: list[str]) -> CollectionProfile:
        mutable = [
            field
            for field in fields
            if field not in {"id", "date_created", "user_created", "date_updated", "user_updated"}
        ]
        return CollectionProfile(
            collection=name,
            primary_key="id",
            fields=fields,
            create_fields=[
                field
                for field in fields
                if field not in {"date_created", "user_created", "date_updated", "user_updated"}
            ],
            update_fields=mutable,
            archive_field="status" if "status" in fields else None,
            date_updated_field="date_updated" if "date_updated" in fields else None,
            allow_permanent_delete=False,
            allow_revision_history=True,
            allow_revision_revert=True,
        )

    def _build_collection_body(
        self,
        name: str,
        display_name: str,
        fields: list[FieldDefinition],
        display_fields: list[FieldDefinition],
    ) -> dict[str, Any]:
        field_payloads: list[dict[str, Any]] = [
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
        ]
        # 系统字段：status (archive 语义)、sort、审计字段。与 meta.archive_field/sort_field 一致。
        field_payloads.extend(self._system_field_payloads())
        for f, display_field in zip(fields, display_fields, strict=True):
            data_type = _TYPE_MAP.get(f.type, "string")
            field_payloads.append(
                {
                    "field": f.key,
                    "type": data_type,
                    "meta": {
                        "field": f.key,
                        "required": False,
                        "translations": translation(display_field.key),
                    },
                    "schema": {"name": f.key, "data_type": data_type, "is_nullable": True},
                }
            )
        return {
            "collection": name,
            "schema": {"name": name},
            "meta": {
                "collection": name,
                "accountability": "all",
                "translations": translation(display_name),
                "archive_field": "status",
                "archive_value": "archived",
                "unarchive_value": "active",
                "archive_app_filter": True,
                "sort_field": "sort",
                "versioning": False,
            },
            "fields": field_payloads,
        }

    @staticmethod
    def _unique_adopted_name(
        preferred: str,
        parent: str | None,
        occupied: set[tuple[str | None, str]],
    ) -> str:
        if (parent, normalize_display_name(preferred)) not in occupied:
            return preferred
        suffix = 2
        while (parent, normalize_display_name(f"{preferred} ({suffix})")) in occupied:
            suffix += 1
        return f"{preferred} ({suffix})"

    @staticmethod
    def _system_field_payloads() -> list[dict[str, Any]]:
        """Directus system fields every user table inherits (archive + audit)."""
        return [
            {
                "field": "status",
                "type": "string",
                "meta": {
                    "field": "status",
                    "required": True,
                    "options": {
                        "choices": [
                            {"text": "Active", "value": "active"},
                            {"text": "Archived", "value": "archived"},
                        ]
                    },
                },
                "schema": {
                    "name": "status",
                    "data_type": "string",
                    "is_nullable": False,
                    "default_value": "active",
                },
            },
            {
                "field": "sort",
                "type": "integer",
                "meta": {"field": "sort", "readonly": True},
                "schema": {"name": "sort", "data_type": "integer", "is_nullable": True},
            },
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
