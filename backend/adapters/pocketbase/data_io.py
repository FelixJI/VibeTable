"""PocketBase composition adapters for paste, import, and export.

The application services predate the product schema catalog and intentionally
consume a small ``CollectionProfile`` port.  This module is the only place that
projects the frozen product table definition into that compatibility profile.
It refreshes the profile before every public operation, so schema revision and
writable-field checks never rely on a startup snapshot.
"""

from __future__ import annotations

import asyncio
import math
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Protocol, cast

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.adapters.pocketbase.relation_io import (
    PocketBaseLookupExportProvider,
    PocketBaseRelationImportProvider,
)
from backend.application.export_service import ExportService, QueryPagePort
from backend.application.import_service import ImportService
from backend.application.paste_service import PasteError, PasteService
from backend.application.task_runtime import CancellationToken, ProgressReporter
from backend.application.task_service import TaskService
from backend.contracts.data_io import (
    ApplyImportParams,
    ApplyImportResult,
    ExportParams,
    ExportResult,
    GenerateTemplateParams,
    ImportPlan,
    PreviewImportParams,
    TemplateResult,
)
from backend.contracts.data_profile import (
    CollectionProfile,
    collection_profile_from_definition,
)
from backend.contracts.paste import (
    ApplyPasteParams,
    ApplyPasteResult,
    PastePlan,
    PreviewPasteParams,
)
from backend.contracts.product_rpc import JsonObject, JsonValue

ProgressCallback = Callable[[int, int, str], Awaitable[None]]
CancellationCheck = Callable[[], bool]


class _SchemaReadClient(Protocol):
    async def describe_table(self, table_id: str) -> JsonObject: ...

    async def read_rows(self, *, table_id: str, row_ids: list[str]) -> list[JsonObject]: ...


@dataclass
class _LocalActor:
    id: str = "local-user"
    first_name: str = ""
    last_name: str = ""


class PocketBaseLocalAuth:
    """Single-user actor used by the local product mutation boundary."""

    async def current_user(self) -> _LocalActor:
        return _LocalActor()


class PocketBasePasteReadPort:
    """Live schema and row reads required by :class:`PasteService`."""

    def __init__(
        self,
        *,
        client: _SchemaReadClient,
        profiles: dict[str, CollectionProfile],
        definitions: dict[str, JsonObject],
    ) -> None:
        self._client = client
        self._profiles = profiles
        self._definitions = definitions

    async def fields(self, profile: CollectionProfile) -> list[JsonObject]:
        definition = self._definition(profile.collection)
        result: list[JsonObject] = []
        for raw in _raw_fields(definition):
            identity = raw.get("identity")
            name = identity.get("physicalName") if isinstance(identity, dict) else None
            if not isinstance(name, str):
                continue
            scale, precision = _precision_scale(raw)
            schema: JsonObject = {
                "data_type": _field_data_type(raw),
                "numeric_scale": scale,
                "numeric_precision": precision,
            }
            result.append({"field": name, "schema": schema})
        return result

    async def readonly_fields(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> set[str]:
        definition = await self._live_definition(profile, refresh=refresh)
        readonly: set[str] = {profile.primary_key}
        for raw in _raw_fields(definition):
            identity = raw.get("identity")
            if not isinstance(identity, dict):
                continue
            physical_name = identity.get("physicalName")
            if not isinstance(physical_name, str):
                continue
            lifecycle = raw.get("lifecycle")
            if (
                definition.get("kind") == "view"
                or raw.get("logicalType") in {"formula", "lookup", "autoDate"}
                or not isinstance(lifecycle, dict)
                or lifecycle.get("state") != "active"
            ):
                readonly.add(physical_name)
        return readonly

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
        allowed = set(current.create_fields if operation == "create" else current.update_fields)
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
    ) -> JsonObject:
        rows = await self._client.read_rows(
            table_id=profile.collection,
            row_ids=[item_id],
        )
        if len(rows) != 1 or str(rows[0].get("id", "")) != item_id:
            raise PasteError("row not found", code="paste_row_not_found")
        return rows[0]

    def _definition(self, collection: str) -> JsonObject:
        definition = self._definitions.get(collection)
        if definition is None:
            raise PasteError("product schema is unavailable", code="schema_unknown")
        return definition

    async def _live_definition(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool,
    ) -> JsonObject:
        if not refresh:
            return self._definition(profile.collection)
        definition = await self._client.describe_table(profile.collection)
        self._definitions[profile.collection] = definition
        self._profiles[profile.collection] = collection_profile_from_definition(definition)
        return definition


class ProductDataIoRuntime:
    """Refresh-aware product façade registered by the Python composition root."""

    def __init__(self, *, client: PocketBaseClient, task_service: TaskService) -> None:
        self._client = client
        self._task_service = task_service
        self._profiles: dict[str, CollectionProfile] = {}
        self._definitions: dict[str, JsonObject] = {}
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

    async def preview_paste(self, params: PreviewPasteParams) -> PastePlan:
        await self._refresh(params.collection)
        return await self._paste.preview(params)

    async def apply_paste(self, params: ApplyPasteParams) -> ApplyPasteResult:
        await self._refresh(params.collection)
        return await self._paste.apply(params)

    async def preview_import(self, params: PreviewImportParams) -> ImportPlan:
        await self._refresh(params.collection)
        return await self._import.preview(params)

    async def apply_import(
        self,
        params: ApplyImportParams,
        *,
        progress: ProgressCallback | None = None,
        cancelled: CancellationCheck | None = None,
    ) -> ApplyImportResult:
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
        progress: ProgressCallback | None = None,
        cancelled: CancellationCheck | None = None,
    ) -> ExportResult:
        await self._refresh(params.collection)
        return await self._export.export(
            params,
            progress=progress,
            cancelled=cancelled,
        )

    async def generate_template(self, params: GenerateTemplateParams) -> TemplateResult:
        await self._refresh(params.collection)
        return await self._export.generate_template(params.collection, params.grant_id)

    def register_tasks(self) -> None:
        async def apply_import_task(
            _task_id: str,
            reporter: ProgressReporter,
            token: CancellationToken,
            raw_params: JsonObject,
        ) -> ApplyImportResult:
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
            reporter: ProgressReporter,
            token: CancellationToken,
            raw_params: JsonObject,
        ) -> ExportResult:
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


def _raw_fields(definition: JsonObject) -> list[JsonObject]:
    raw = definition.get("fields")
    if not isinstance(raw, list):
        raise ValueError("product schema fields are invalid")
    return [_json_object(item, "product schema field") for item in raw]


def _precision_scale(field: JsonObject) -> tuple[int | None, int | None]:
    display = field.get("display")
    if not isinstance(display, dict):
        return None, None
    scale = display.get("displayScale")
    return (
        scale if isinstance(scale, int) and not isinstance(scale, bool) else None,
        None,
    )


def _field_data_type(field: JsonObject) -> str | None:
    """Expose the normalized product type needed by paste coercion.

    Numeric metadata still carries precision/scale, while structured types
    such as JSON must remain visible so paste cannot silently store their wire
    text as a JSON string.
    """
    value = field.get("logicalType")
    if value == "number":
        storage = field.get("storage")
        options = storage.get("options") if isinstance(storage, dict) else None
        return (
            "integer" if isinstance(options, dict) and options.get("onlyInt") is True else "number"
        )
    return str(value) if isinstance(value, str) and value else None


def _json_object(value: object, label: str) -> JsonObject:
    if not isinstance(value, dict) or not all(isinstance(key, str) for key in value):
        raise ValueError(f"{label} JSON object keys must be strings")
    return {key: _json_value(item, label) for key, item in value.items()}


def _json_value(value: object, label: str) -> JsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"{label} numbers must be finite")
        return value
    if isinstance(value, list):
        return [_json_value(item, label) for item in value]
    if isinstance(value, dict):
        return _json_object(value, label)
    raise ValueError(f"{label} must contain only JSON values")


__all__ = [
    "PocketBaseLocalAuth",
    "PocketBasePasteReadPort",
    "ProductDataIoRuntime",
    "collection_profile_from_definition",
]
