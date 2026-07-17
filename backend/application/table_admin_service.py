from __future__ import annotations

from typing import Any, Protocol

from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.table_admin import (
    CreateTableParams,
    CreateTableResult,
    DeleteTableParams,
    DeleteTableResult,
    FieldDefinition,
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
    "integer": "integer",
    "decimal": "decimal",
    "date": "date",
    "boolean": "boolean",
    "text": "text",
}

_SYSTEM_FIELDS = ["status", "sort", "date_created", "user_created", "date_updated", "user_updated"]
# The whole ``vibetable_`` namespace is reserved for system/bootstrap tables
# (vibetable_workspaces, vibetable_documents, vibetable_settings, ...). Matching
# the full prefix (rather than enumerating each singular substring like
# ``vibetable_document``) also covers system tables added later without silently
# letting a user shadow them. ``directus_`` covers all Directus internal tables.
_RESERVED_PREFIXES = ("directus_", "vibetable_")


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
    ) -> None:
        self._transport = transport
        self._auth = auth
        # 运行时 manifest 的 by_collection 视图（可变 dict）。
        # 真实 CapabilityManifest.by_collection 是只读计算属性（frozen 模型），
        # DirectusService 传入了它已维护的可变 ``_profiles`` 快照。
        # create_table/delete_table 同步增删该快照（写入真正的 CollectionProfile），
        # 使建表后当前会话立即可用；重启时 manifest 从 Directus 重读。
        self._profiles: dict[str, CollectionProfile] = profiles

    async def create_table(self, params: CreateTableParams) -> CreateTableResult:
        name = params.name
        self._validate_name(name)
        # Build the runtime profile BEFORE touching Directus. If profile
        # construction fails (e.g. the identifier rules in CollectionProfile
        # reject a name the contract layer somehow let through), we raise here
        # and never POST /collections — otherwise Directus would create the
        # collection and we would leave an orphan behind when this call fails.
        # ValidationError is wrapped as TableAdminError so the dispatcher maps
        # it to the registered table_admin code (-32110) instead of falling
        # through to the opaque -32603 "Internal error" bucket.
        try:
            profile = self._profile_for(name, params.fields)
        except TableAdminError:
            raise
        except Exception as exc:
            raise TableAdminError(
                f"cannot build capability profile for {name!r}: {exc}",
                code="profile_build_failed",
            ) from exc
        token = await self._auth.access_token()
        collection_body = self._build_collection_body(name, params.fields)
        await self._transport.request(
            "POST", "/collections", access_token=token, json_body=collection_body,
            expected_status=(200, 201),
        )
        # 同步更新运行时 manifest 白名单：写入真正的 CollectionProfile，
        # 使建表后当前会话内即可对该表执行 read/create/update（_profiles 查询生效）。
        self._profiles[name] = profile
        return CreateTableResult(collection=name, primary_key=profile.primary_key, fields=list(profile.fields))

    async def delete_table(self, params: DeleteTableParams) -> DeleteTableResult:
        name = params.name
        self._validate_name(name)
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE", f"/collections/{name}", access_token=token,
            expected_status=(200, 204),
        )
        self._profiles.pop(name, None)
        return DeleteTableResult(collection=name, deleted=True)

    def _validate_name(self, name: str) -> None:
        if name.startswith(_RESERVED_PREFIXES):
            raise TableAdminError(
                f"collection name {name!r} is reserved or conflicts with system tables",
                code="table_name_reserved",
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
            create_fields=["id", *_SYSTEM_FIELDS, *declared],
            update_fields=[*declared, *_SYSTEM_FIELDS],
            archive_field="status",
            archive_value="archived",
            restore_value="active",
            date_updated_field="date_updated",
            allow_permanent_delete=True,
        )

    def _build_collection_body(self, name: str, fields: list) -> dict[str, Any]:
        field_payloads: list[dict[str, Any]] = [
            {"field": "id", "type": "uuid", "meta": {"required": True, "readonly": True},
             "schema": {"name": "id", "data_type": "uuid", "is_primary_key": True, "is_nullable": False}},
        ]
        # 系统字段：status (archive 语义)、sort、审计字段。与 meta.archive_field/sort_field 一致。
        field_payloads.extend(self._system_field_payloads())
        for f in fields:
            data_type = _TYPE_MAP.get(f.type, "string")
            field_payloads.append({
                "field": f.key, "type": data_type,
                "meta": {"field": f.key, "required": False},
                "schema": {"name": f.key, "data_type": data_type, "is_nullable": True},
            })
        return {
            "collection": name,
            "schema": {"name": name},
            "meta": {
                "collection": name, "accountability": "all",
                "archive_field": "status", "archive_value": "archived", "unarchive_value": "active",
                "archive_app_filter": True, "sort_field": "sort", "versioning": False,
            },
            "fields": field_payloads,
        }

    @staticmethod
    def _system_field_payloads() -> list[dict[str, Any]]:
        """Directus system fields every user table inherits (archive + audit)."""
        return [
            {"field": "status", "type": "string",
             "meta": {"field": "status", "required": True, "options": {
                 "choices": [{"text": "Active", "value": "active"}, {"text": "Archived", "value": "archived"}]}},
             "schema": {"name": "status", "data_type": "string", "is_nullable": False, "default_value": "active"}},
            {"field": "sort", "type": "integer",
             "meta": {"field": "sort", "readonly": True},
             "schema": {"name": "sort", "data_type": "integer", "is_nullable": True}},
            {"field": "date_created", "type": "timestamp",
             "meta": {"field": "date_created", "readonly": True, "special": ["date-created"]},
             "schema": {"name": "date_created", "data_type": "timestamp", "is_nullable": True}},
            {"field": "user_created", "type": "uuid",
             "meta": {"field": "user_created", "readonly": True, "special": ["user-created"]},
             "schema": {"name": "user_created", "data_type": "uuid", "is_nullable": True}},
            {"field": "date_updated", "type": "timestamp",
             "meta": {"field": "date_updated", "readonly": True, "special": ["date-updated"]},
             "schema": {"name": "date_updated", "data_type": "timestamp", "is_nullable": True}},
            {"field": "user_updated", "type": "uuid",
             "meta": {"field": "user_updated", "readonly": True, "special": ["user-updated"]},
             "schema": {"name": "user_updated", "data_type": "uuid", "is_nullable": True}},
        ]
