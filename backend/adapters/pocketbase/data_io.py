"""PocketBase composition adapters for paste, import, and export.

The application services predate the product schema catalog and intentionally
consume a small ``CollectionProfile`` port.  This module is the only place that
projects the frozen product table definition into that compatibility profile.
It refreshes the profile before every public operation, so schema revision and
writable-field checks never rely on a startup snapshot.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any, cast

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.application.export_service import ExportService, QueryPagePort
from backend.application.import_service import ImportService
from backend.application.paste_service import PasteError, PasteService
from backend.application.relation_io_adapters import (
    PocketBaseLookupExportProvider,
    PocketBaseRelationImportProvider,
)
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    GenerateTemplateParams,
    PreviewImportParams,
)
from backend.contracts.data_profile import (
    CollectionProfile,
    collection_profile_from_definition,
)
from backend.contracts.paste import ApplyPasteParams, PreviewPasteParams


@dataclass(frozen=True)
class _LocalActor:
    id: str = "local-user"
    first_name: str = ""
    last_name: str = ""


class PocketBaseLocalAuth:
    """Single-user actor used by the local product mutation boundary."""

    async def current_user(self) -> Any:
        return _LocalActor()


class PocketBasePasteReadPort:
    """Live schema and row reads required by :class:`PasteService`."""

    def __init__(
        self,
        *,
        client: PocketBaseClient,
        profiles: dict[str, CollectionProfile],
        definitions: dict[str, dict[str, Any]],
    ) -> None:
        self._client = client
        self._profiles = profiles
        self._definitions = definitions

    async def fields(self, profile: CollectionProfile) -> list[dict[str, Any]]:
        definition = self._definition(profile.collection)
        result: list[dict[str, Any]] = []
        for raw in _raw_fields(definition):
            name = raw.get("physicalName")
            if not isinstance(name, str):
                continue
            scale, precision = _precision_scale(raw)
            result.append(
                {
                    "field": name,
                    "schema": {
                        "data_type": _field_data_type(raw.get("dataType")),
                        "numeric_scale": scale,
                        "numeric_precision": precision,
                    },
                }
            )
        return result

    async def readonly_fields(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> set[str]:
        definition = await self._live_definition(profile, refresh=refresh)
        return {
            str(raw["physicalName"])
            for raw in _raw_fields(definition)
            if raw.get("readOnly") is True and isinstance(raw.get("physicalName"), str)
        } | {profile.primary_key}

    async def require_write_fields(
        self,
        profile: CollectionProfile,
        fields: set[str],
        *,
        operation: str,
        refresh: bool,
    ) -> None:
        definition = await self._live_definition(profile, refresh=refresh)
        current = collection_profile_from_definition(definition)
        if current.capability_hash != profile.capability_hash:
            raise PasteError("schema changed", code="schema_mismatch")
        allowed = set(
            current.create_fields if operation == "create" else current.update_fields
        )
        denied = fields - allowed
        if denied:
            raise PasteError(
                "field policy changed",
                code="schema_mismatch",
                data={"fields": sorted(denied)},
            )

    async def read_item(
        self,
        profile: CollectionProfile,
        item_id: str,
    ) -> dict[str, Any]:
        rows = await self._client.read_rows(
            table_id=profile.collection,
            row_ids=[item_id],
        )
        if len(rows) != 1 or str(rows[0].get("id", "")) != item_id:
            raise PasteError("row not found", code="paste_row_not_found")
        return rows[0]

    def _definition(self, collection: str) -> dict[str, Any]:
        definition = self._definitions.get(collection)
        if definition is None:
            raise PasteError("product schema is unavailable", code="schema_unknown")
        return definition

    async def _live_definition(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> dict[str, Any]:
        if not refresh:
            return self._definition(profile.collection)
        definition = await self._client.describe_table(profile.collection)
        self._definitions[profile.collection] = definition
        self._profiles[profile.collection] = collection_profile_from_definition(definition)
        return definition


class ProductDataIoRuntime:
    """Refresh-aware product façade registered by the Python composition root."""

    def __init__(self, *, client: PocketBaseClient, task_service: Any) -> None:
        self._client = client
        self._task_service = task_service
        self._profiles: dict[str, CollectionProfile] = {}
        self._definitions: dict[str, dict[str, Any]] = {}
        self._refresh_lock = asyncio.Lock()
        auth = PocketBaseLocalAuth()
        read_port = PocketBasePasteReadPort(
            client=client,
            profiles=self._profiles,
            definitions=self._definitions,
        )
        bulk = PocketBaseBulkMutationClient(client=client, auth=auth)
        self._paste = PasteService(
            client=read_port,
            auth=auth,
            bulk=bulk,
            profiles=self._profiles,
            project="local",
        )
        self._import = ImportService(
            client=read_port,
            auth=auth,
            bulk=bulk,
            profiles=self._profiles,
            resolve_path=task_service.resolve_path,
            consume_grant=task_service.consume_grant,
            relation_provider=PocketBaseRelationImportProvider(
                client=client,
                bulk=bulk,
            ),
        )
        self._export = ExportService(
            query_port=cast(QueryPagePort, client),
            profiles=self._profiles,
            resolve_path=task_service.resolve_path,
            lookup_provider=PocketBaseLookupExportProvider(client=client),
        )

    @property
    def profiles(self) -> dict[str, CollectionProfile]:
        return self._profiles

    async def preview_paste(self, params: PreviewPasteParams) -> Any:
        await self._refresh(params.collection)
        return await self._paste.preview(params)

    async def apply_paste(self, params: ApplyPasteParams) -> Any:
        await self._refresh(params.collection)
        return await self._paste.apply(params)

    async def preview_import(self, params: PreviewImportParams) -> Any:
        await self._refresh(params.collection)
        return await self._import.preview(params)

    async def apply_import(
        self,
        params: ApplyImportParams,
        *,
        progress: Any = None,
        cancelled: Any = None,
    ) -> Any:
        await self._refresh(params.collection)
        return await self._import.apply(
            params,
            progress=progress,
            cancelled=cancelled,
        )

    async def export(
        self,
        params: ExportParams,
        *,
        progress: Any = None,
        cancelled: Any = None,
    ) -> Any:
        await self._refresh(params.collection)
        return await self._export.export(
            params,
            progress=progress,
            cancelled=cancelled,
        )

    async def generate_template(self, params: GenerateTemplateParams) -> Any:
        await self._refresh(params.collection)
        return await self._export.generate_template(params.collection, params.grant_id)

    def register_tasks(self) -> None:
        async def apply_import_task(
            _task_id: str,
            reporter: Any,
            token: Any,
            raw_params: dict[str, Any],
        ) -> Any:
            params = ApplyImportParams.model_validate(raw_params)

            async def progress(done: int, total: int, message: str) -> None:
                await reporter.report(done=done, total=total, message=message)

            return await self.apply_import(
                params,
                progress=progress,
                cancelled=lambda: token.cancelled,
            )

        async def export_task(
            _task_id: str,
            reporter: Any,
            token: Any,
            raw_params: dict[str, Any],
        ) -> Any:
            params = ExportParams.model_validate(raw_params)

            async def progress(done: int, total: int, message: str) -> None:
                await reporter.report(done=done, total=total, message=message)

            return await self.export(
                params,
                progress=progress,
                cancelled=lambda: token.cancelled,
            )

        self._task_service.runtime.register("data.import", apply_import_task)
        self._task_service.runtime.register("data.export", export_task)

    async def _refresh(self, collection: str) -> CollectionProfile:
        async with self._refresh_lock:
            definition = await self._client.describe_table(collection)
            profile = collection_profile_from_definition(definition)
            if profile.collection != collection:
                raise PasteError(
                    "product schema identity mismatch",
                    code="schema_mismatch",
                )
            self._definitions[collection] = definition
            self._profiles[collection] = profile
            return profile


def _raw_fields(definition: dict[str, Any]) -> list[dict[str, Any]]:
    raw = definition.get("fields")
    if not isinstance(raw, list) or not all(isinstance(item, dict) for item in raw):
        raise ValueError("product schema fields are invalid")
    return raw


def _required_text(value: dict[str, Any], key: str) -> str:
    result = value.get(key)
    if not isinstance(result, str) or not result:
        raise ValueError(f"product schema {key} is invalid")
    return result


def _precision_scale(field: dict[str, Any]) -> tuple[int | None, int | None]:
    constraints = field.get("constraints")
    if not isinstance(constraints, list):
        return None, None
    for constraint in constraints:
        if not isinstance(constraint, dict) or constraint.get("kind") != "precisionScale":
            continue
        scale = constraint.get("scale")
        precision = constraint.get("precision")
        return (
            scale if isinstance(scale, int) and not isinstance(scale, bool) else None,
            precision if isinstance(precision, int) and not isinstance(precision, bool) else None,
        )
    return None, None


def _field_data_type(value: Any) -> str | None:
    """Expose the normalized product type needed by paste coercion.

    Numeric metadata still carries precision/scale, while structured types
    such as JSON must remain visible so paste cannot silently store their wire
    text as a JSON string.
    """
    return str(value) if isinstance(value, str) and value else None


__all__ = [
    "PocketBaseLocalAuth",
    "PocketBasePasteReadPort",
    "ProductDataIoRuntime",
    "collection_profile_from_definition",
]
