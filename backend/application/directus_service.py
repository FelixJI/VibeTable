"""B4 application service exposing Directus-first capabilities over JSON-RPC."""

from __future__ import annotations

import asyncio
import contextlib
import hashlib
import json
import os
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit, urlunsplit

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker, SessionStatus
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.coerce import validate_number_field
from backend.adapters.directus.contracts import DirectusSourceConfig
from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.profile import CapabilityManifest, CollectionProfile
from backend.adapters.directus.realtime import (
    DirectusChangeEvent,
    DirectusRealtimeSupervisor,
    SubscriptionSpec,
    WebsocketsConnector,
)
from backend.adapters.directus.relation_schema import normalize_directus_relations
from backend.adapters.directus.schema import build_directus_schema
from backend.adapters.directus.secrets import DpapiFileSecretStore
from backend.adapters.directus.transport import StdlibDirectusTransport
from backend.application.collaboration_service import CollaborationService
from backend.application.document_workspace_service import DocumentWorkspaceService
from backend.application.export_service import ExportService
from backend.application.file_tools_service import FileToolsService
from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.history_service import HistoryService
from backend.application.import_service import ImportService
from backend.application.insights_service import InsightsService
from backend.application.lookup_service import LookupService
from backend.application.paste_service import (
    BulkMutationClient,
    PasteService,
    PasteTokenStore,
)
from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_interaction_broker import PluginInteractionBroker
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry
from backend.application.relation_data_service import RelationDataService
from backend.application.relation_io_adapters import (
    DirectusRelationImportProvider,
    LookupExportProvider,
)
from backend.application.relation_schema_service import RelationSchemaService
from backend.application.settings_command_service import SettingsCommandService
from backend.application.table_admin_service import TableAdminService
from backend.contracts.directus import (
    DirectusCollectionListResult,
    DirectusCollectionParams,
    DirectusCreateParams,
    DirectusEmptyParams,
    DirectusItemParams,
    DirectusItemResult,
    DirectusLoginParams,
    DirectusPageResult,
    DirectusReadParams,
    DirectusSchemaResult,
    DirectusServerInfoResult,
    DirectusSubscribeParams,
    DirectusSubscriptionResult,
    DirectusUnsubscribeParams,
    DirectusUpdateParams,
)
from backend.contracts.lookup import LookupCollectionParams, LookupDefinition
from backend.contracts.relation import (
    RelationColumn,
    RelationProjectionParams,
    RelationProjectionResult,
)
from backend.contracts.relation_admin import (
    NormalizedRelationDescriptor,
    RelationLookupCapabilities,
    SchemaDescribeParams,
    SchemaDescribeResult,
    SchemaSnapshot,
)
from backend.contracts.table import ColumnSchema
from backend.infrastructure.directus_flow import DirectusFlowAdapter
from backend.infrastructure.directus_interaction import DirectusInteractionAdapter
from backend.infrastructure.plugin_file_capability import HostFileCapabilityAdapter
from backend.infrastructure.plugin_interaction import HostConfirmationAdapter
from backend.infrastructure.plugin_store import PluginProjectStore
from backend.infrastructure.plugin_worker import (
    DirectusBulkMutationAdapter,
    NodePluginWorkerAdapter,
)

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"


class DirectusService:
    def __init__(
        self,
        manifest: CapabilityManifest,
        auth: DirectusAuthBroker,
        client: DirectusClient,
        realtime: DirectusRealtimeSupervisor | None = None,
    ) -> None:
        self._manifest = manifest
        self._profiles = manifest.by_collection
        self._auth = auth
        self._client = client
        self._realtime = realtime
        self._notification_sink: Callable[[DirectusChangeEvent], Awaitable[None]] | None = None
        self._subscriptions: dict[str, tuple[CollectionProfile, SubscriptionSpec]] = {}
        self._realtime_task: asyncio.Task[None] | None = None
        self._realtime_stop: asyncio.Event | None = None
        self._realtime_lock = asyncio.Lock()
        self._paste_service: PasteService | None = None
        self._import_service: ImportService | None = None
        self._export_service: ExportService | None = None
        self._collaboration_service: CollaborationService | None = None
        self._insights_service: InsightsService | None = None
        self._lookup_service: LookupService | None = None
        self._relation_service: RelationDataService | None = None
        self._relation_schema_service: RelationSchemaService | None = None
        self._schema_snapshots: dict[str, SchemaSnapshot] = {}
        self._file_tools_service: FileToolsService | None = None
        self._settings_command_service: SettingsCommandService | None = None
        self._history_service: HistoryService | None = None
        self._document_workspace_service: DocumentWorkspaceService | None = None
        self._table_admin_service: TableAdminService | None = None
        self._plugin_service: PluginPlatformService | None = None
        self._plugin_store: PluginProjectStore | None = None
        self._plugin_runtime: PluginExecutionRuntime | None = None
        self._plugin_confirmation: HostConfirmationAdapter | None = None
        self._plugin_files: HostFileCapabilityAdapter | None = None

    @property
    def paste_service(self) -> PasteService | None:
        return self._paste_service

    @paste_service.setter
    def paste_service(self, value: PasteService | None) -> None:
        self._paste_service = value

    @property
    def import_service(self) -> ImportService | None:
        return self._import_service

    @import_service.setter
    def import_service(self, value: ImportService | None) -> None:
        self._import_service = value

    @property
    def export_service(self) -> ExportService | None:
        return self._export_service

    @export_service.setter
    def export_service(self, value: ExportService | None) -> None:
        self._export_service = value

    @property
    def collaboration_service(self) -> CollaborationService | None:
        return self._collaboration_service

    @collaboration_service.setter
    def collaboration_service(self, value: CollaborationService | None) -> None:
        self._collaboration_service = value

    @property
    def history_service(self) -> HistoryService | None:
        return self._history_service

    @history_service.setter
    def history_service(self, value: HistoryService | None) -> None:
        self._history_service = value

    @property
    def document_workspace_service(self) -> DocumentWorkspaceService | None:
        return self._document_workspace_service

    @document_workspace_service.setter
    def document_workspace_service(self, value: DocumentWorkspaceService | None) -> None:
        self._document_workspace_service = value

    @property
    def table_admin_service(self) -> TableAdminService | None:
        return self._table_admin_service

    @table_admin_service.setter
    def table_admin_service(self, value: TableAdminService | None) -> None:
        self._table_admin_service = value

    @property
    def insights_service(self) -> InsightsService | None:
        return self._insights_service

    @insights_service.setter
    def insights_service(self, value: InsightsService | None) -> None:
        self._insights_service = value

    @property
    def lookup_service(self) -> LookupService | None:
        return self._lookup_service

    @lookup_service.setter
    def lookup_service(self, value: LookupService | None) -> None:
        self._lookup_service = value

    @property
    def relation_service(self) -> RelationDataService | None:
        return self._relation_service

    @relation_service.setter
    def relation_service(self, value: RelationDataService | None) -> None:
        self._relation_service = value

    @property
    def relation_schema_service(self) -> RelationSchemaService | None:
        return self._relation_schema_service

    @relation_schema_service.setter
    def relation_schema_service(self, value: RelationSchemaService | None) -> None:
        self._relation_schema_service = value

    @property
    def file_tools_service(self) -> FileToolsService | None:
        return self._file_tools_service

    @file_tools_service.setter
    def file_tools_service(self, value: FileToolsService | None) -> None:
        self._file_tools_service = value

    @property
    def settings_command_service(self) -> SettingsCommandService | None:
        return self._settings_command_service

    @settings_command_service.setter
    def settings_command_service(self, value: SettingsCommandService | None) -> None:
        self._settings_command_service = value

    @property
    def plugin_service(self) -> PluginPlatformService | None:
        return self._plugin_service

    @plugin_service.setter
    def plugin_service(self, value: PluginPlatformService | None) -> None:
        self._plugin_service = value

    def set_notification_sink(self, sink: Callable[[DirectusChangeEvent], Awaitable[None]]) -> None:
        self._notification_sink = sink

    def set_plugin_notification_sink(self, sink: Callable[[Any], Awaitable[None]]) -> None:
        if self._plugin_runtime is not None:
            self._plugin_runtime.set_notification_sink(sink)
        if self._plugin_confirmation is not None:
            self._plugin_confirmation.set_notification_sink(sink)
        if self._plugin_service is not None:
            self._plugin_service.set_notification_sink(sink)

    def set_plugin_file_notification_sink(self, sink: Callable[[Any], Awaitable[None]]) -> None:
        if self._plugin_files is not None:
            self._plugin_files.set_notification_sink(sink)

    async def login(self, params: DirectusLoginParams) -> SessionStatus:
        return await self._auth.login(params.email, params.password, params.otp)

    async def refresh(self, params: DirectusEmptyParams) -> SessionStatus:
        return await self._auth.refresh()

    async def logout(self, params: DirectusEmptyParams) -> SessionStatus:
        return await self._auth.logout()

    async def status(self, params: DirectusEmptyParams) -> SessionStatus:
        return self._auth.status()

    async def current_user(self, params: DirectusEmptyParams) -> CurrentUser:
        return await self._auth.current_user()

    async def server_info(self, params: DirectusEmptyParams) -> DirectusServerInfoResult:
        payload = await self._client.server_info()
        project = payload.get("project")
        directus = payload.get("directus")
        return DirectusServerInfoResult(
            project_name=_nested_string(project, "project_name", "name"),
            directus_version=(
                payload.get("version")
                if isinstance(payload.get("version"), str)
                else _nested_string(directus, "version")
            ),
            compatibility=self._manifest.directus_compatibility,
        )

    async def list_collections(self, params: DirectusEmptyParams) -> DirectusCollectionListResult:
        # Collection discovery is the connection-readiness boundary. Keep it
        # read-only and fast: a full identifier reconcile can issue many schema
        # reads and registry writes, so the desktop host runs that explicitly
        # after publishing its first database.opened snapshot.
        display_names = (
            self._table_admin_service.display_names if self._table_admin_service is not None else {}
        )
        # Hidden collections (the built-in vibetable_* workspace tables) are
        # valid schema/capability profiles but must not surface in the sidebar.
        # They remain reachable via `directus.schema` and internal flows; only
        # the "list of user-facing tables" excludes them.
        visible = [name for name, profile in self._profiles.items() if not profile.hidden]
        return DirectusCollectionListResult(
            collections=sorted(visible),
            capability_hashes={
                name: profile.capability_hash
                for name, profile in self._profiles.items()
                if not profile.hidden
            },
            display_names={name: display_names.get(name, name) for name in visible},
        )

    async def schema(self, params: DirectusCollectionParams) -> DirectusSchemaResult:
        profile = self._profile(params.collection)
        snapshot, raw_relations = await asyncio.gather(
            self._build_schema_snapshot(profile.collection),
            self._client.relations(profile),
        )
        result_revision = _canonical_revision(
            {
                "schema": snapshot.schema_revision,
                "lookup": snapshot.lookup_revision,
            }
        )
        return DirectusSchemaResult(
            collection=profile.collection,
            primary_key=snapshot.primary_key,
            columns=snapshot.columns,
            relations=raw_relations,
            schema_revision=result_revision,
            capability_hash=profile.capability_hash,
        )

    async def describe_schema(self, params: SchemaDescribeParams) -> SchemaDescribeResult:
        """Negotiate relation/Lookup capabilities and return a live schema."""
        requested = set(params.accepts)
        supported = {
            "vibetable.relation-capabilities.v1",
            "vibetable.lookup-query.v1",
        }
        if requested - supported:
            raise DirectusSchemaError("schema.describe requested an unsupported contract")
        snapshot, raw_capabilities = await asyncio.gather(
            self._build_schema_snapshot(params.collection),
            self._client.relation_lookup_capabilities(),
        )
        return SchemaDescribeResult(
            collection=params.collection,
            request_generation=params.request_generation,
            schema=snapshot,
            capabilities=RelationLookupCapabilities.model_validate(raw_capabilities),
        )

    async def _build_schema_snapshot(self, collection: str) -> SchemaSnapshot:
        """Build a normalized schema with four independent revisions."""
        profile = self._profile(collection)
        fields, relations, permissions = await asyncio.gather(
            self._client.schema_fields(),
            self._client.schema_relations(),
            self._client.permission_snapshot(),
        )
        collection_fields = [
            field for field in fields if field.get("collection") == profile.collection
        ]
        base = build_directus_schema(
            collection=profile.collection,
            fields=collection_fields,
            collection_permissions={"read": {"access": "full", "fields": profile.fields}},
        )
        discovery = normalize_directus_relations(fields=fields, relations=relations)
        normalized = [
            relation
            for relation in discovery.relations
            if relation.source_collection == profile.collection
        ]
        relation_payload = [
            relation.model_dump(mode="json", by_alias=True) for relation in normalized
        ]
        schema_revision = _canonical_revision(
            {"base": base.schema_revision, "relations": relation_payload}
        )
        permission_revision = _canonical_revision(permissions)
        lookup_revision = _canonical_revision([])
        lookup_definitions: list[LookupDefinition] = []
        if self._lookup_service is not None:
            lookup_result = await self._lookup_service.list(
                LookupCollectionParams(collection=profile.collection)
            )
            lookup_revision = lookup_result.lookup_revision
            lookup_definitions = lookup_result.definitions
        readonly = set(base.readonly_fields)
        update_fields = set(profile.update_fields)
        snapshot = SchemaSnapshot(
            collection=profile.collection,
            primary_key=base.primary_key,
            columns=[
                *_decorate_physical_columns(
                    collection=profile.collection,
                    columns=base.columns,
                    relations=normalized,
                    update_fields=update_fields,
                    readonly_fields=readonly,
                ),
                *_lookup_columns(lookup_definitions),
            ],
            normalized_relations=normalized,
            schema_revision=schema_revision,
            permission_revision=permission_revision,
            capability_hash=profile.capability_hash,
            lookup_revision=lookup_revision,
        )
        self._schema_snapshots[profile.collection] = snapshot
        return snapshot

    async def resolve_relation(self, relation_id: str) -> tuple[SchemaSnapshot, Any]:
        """Resolve a stable relation id and refresh its owning live schema."""
        owner: str | None = None
        for collection, snapshot in self._schema_snapshots.items():
            if any(item.relation_id == relation_id for item in snapshot.normalized_relations):
                owner = collection
                break
        if owner is None and "." in relation_id:
            candidate = relation_id.split(".", 1)[0]
            if candidate in self._profiles:
                owner = candidate
        if owner is None:
            # Stable Directus meta ids do not encode the collection. Discover
            # visible profiles until the owner is located; fail closed if not.
            for collection in self._profiles:
                snapshot = await self._build_schema_snapshot(collection)
                if any(item.relation_id == relation_id for item in snapshot.normalized_relations):
                    owner = collection
                    break
        if owner is None:
            raise DirectusSchemaError(f"relation {relation_id!r} is not visible")
        snapshot = await self._build_schema_snapshot(owner)
        relation = next(
            (item for item in snapshot.normalized_relations if item.relation_id == relation_id),
            None,
        )
        if relation is None:
            raise DirectusSchemaError("relation schema changed")
        return snapshot, relation

    async def read(self, params: DirectusReadParams) -> DirectusPageResult:
        profile = self._profile(params.collection)
        rows, meta, plan = await self._client.read_items(
            profile,
            params.query,
            include_archived=params.include_archived,
        )
        for row in rows:
            row["rowKey"] = row.get(profile.primary_key)
        return DirectusPageResult(
            collection=profile.collection,
            rows=rows,
            offset=params.query.offset,
            limit=params.query.limit,
            filtered_rows=_optional_int(meta.get("filter_count")),
            total_rows=_optional_int(meta.get("total_count")),
            semantic_gaps=plan.semantic_gaps,
            capability_hash=profile.capability_hash,
        )

    async def relation_projection(
        self, params: RelationProjectionParams
    ) -> RelationProjectionResult:
        """C1 Task 5: expand declared relations as display columns.

        Only relations declared in the capability manifest are expanded; the
        caller cannot pick arbitrary tables/fields/join types. Relations the
        current user cannot read are listed in ``restricted_relations`` rather
        than faked as empty.
        """
        profile = self._profile(params.collection)
        declared = {r.field: r for r in profile.relations}
        requested = set(params.relations)
        unknown = requested - set(declared)
        if unknown:
            raise DirectusSchemaError(
                f"relations not declared for {params.collection!r}: {', '.join(sorted(unknown))}",
            )
        # Build the deep field list: base fields + relation.display_fields.
        fields = list(profile.fields)
        relation_columns: list[RelationColumn] = []
        for field in params.relations:
            relation = declared[field]
            if relation.related_collection is None:
                raise DirectusSchemaError(
                    f"relation {relation.field!r} has no declared related collection"
                )
            for display in relation.display_fields[: params.max_depth * 4]:
                deep = f"{relation.field}.{display}"
                if deep not in fields:
                    fields.append(deep)
                relation_columns.append(
                    RelationColumn(
                        relation=relation.field,
                        field=display,
                        related_collection=relation.related_collection,
                        display_path=deep,
                    )
                )
        rows, _meta, _plan = await self._client.read_items_with_fields(
            profile, params.query, fields
        )
        for row in rows:
            row["rowKey"] = row.get(profile.primary_key)
        return RelationProjectionResult(
            collection=profile.collection,
            rows=rows,
            relation_columns=relation_columns,
            restricted_relations=[],
            capability_hash=profile.capability_hash,
        )

    async def create(self, params: DirectusCreateParams) -> DirectusItemResult:
        profile = self._profile(params.collection)
        await self._validate_numeric_values(profile, params.values)
        item = await self._client.create_item(
            profile,
            params.values,
            request_id=params.request_id,
        )
        return DirectusItemResult(collection=profile.collection, item=item)

    async def update(self, params: DirectusUpdateParams) -> DirectusItemResult:
        profile = self._profile(params.collection)
        await self._validate_numeric_values(profile, params.values)
        item = await self._client.update_item(
            profile,
            params.item_id,
            params.values,
            expected_date_updated=params.expected_date_updated,
            request_id=params.request_id,
        )
        return DirectusItemResult(collection=profile.collection, item=item)

    async def _validate_numeric_values(
        self, profile: CollectionProfile, values: dict[str, Any]
    ) -> None:
        """Reject numeric writes that exceed a column's scale/precision.

        Backstop for the frontend's local validation: catches values that would
        otherwise be silently truncated by the database (e.g. 3.14159 into a
        2-digit decimal column, or via direct API access bypassing the grid).
        No-op for empty writes or non-numeric columns.
        """
        numeric_values = {name: value for name, value in values.items() if value is not None}
        if not numeric_values:
            return
        fields = await self._client.fields(profile)
        schema = build_directus_schema(
            collection=profile.collection,
            fields=fields,
            collection_permissions={"read": {"access": "full", "fields": profile.fields}},
        )
        columns = {column.name: column for column in schema.columns}
        for name, value in numeric_values.items():
            column = columns.get(name)
            if column is None:
                continue
            validate_number_field(
                value,
                data_type=column.data_type,
                scale=column.scale,
                precision=column.precision,
                field_name=name,
            )

    async def archive(self, params: DirectusItemParams) -> DirectusItemResult:
        profile = self._profile(params.collection)
        item = await self._client.archive_item(profile, params.item_id)
        return DirectusItemResult(collection=profile.collection, item=item)

    async def restore(self, params: DirectusItemParams) -> DirectusItemResult:
        profile = self._profile(params.collection)
        item = await self._client.restore_item(profile, params.item_id)
        return DirectusItemResult(collection=profile.collection, item=item)

    async def delete(self, params: DirectusItemParams) -> DirectusItemResult:
        profile = self._profile(params.collection)
        await self._client.delete_item(profile, params.item_id)
        return DirectusItemResult(
            collection=profile.collection,
            item={profile.primary_key: params.item_id},
        )

    async def subscribe(self, params: DirectusSubscribeParams) -> DirectusSubscriptionResult:
        if self._realtime is None:
            raise RuntimeError("Directus Realtime is not configured")
        profile = self._profile(params.collection)
        spec = SubscriptionSpec(
            uid=params.uid,
            collection=profile.collection,
            fields=params.fields,
        )
        denied = set(spec.fields) - set(profile.fields)
        if denied:
            raise ValueError("subscription requested fields outside profile allowlist")
        async with self._realtime_lock:
            existing = self._subscriptions.get(spec.uid)
            if existing is not None and existing[1] != spec:
                raise ValueError("subscription uid is already in use")
            self._subscriptions[spec.uid] = (profile, spec)
            await self._restart_realtime_locked()
        return DirectusSubscriptionResult(
            uid=spec.uid,
            collection=profile.collection,
            active=True,
        )

    async def unsubscribe(self, params: DirectusUnsubscribeParams) -> DirectusSubscriptionResult:
        async with self._realtime_lock:
            existing = self._subscriptions.pop(params.uid, None)
            await self._restart_realtime_locked()
        return DirectusSubscriptionResult(
            uid=params.uid,
            collection=existing[0].collection if existing else None,
            active=False,
        )

    async def close(self) -> None:
        async with self._realtime_lock:
            self._subscriptions.clear()
            await self._stop_realtime_locked()
        if self._plugin_service is not None:
            await self._plugin_service.close()
        if self._plugin_store is not None:
            self._plugin_store.close()
            self._plugin_store = None

    async def _restart_realtime_locked(self) -> None:
        await self._stop_realtime_locked()
        if not self._subscriptions or self._realtime is None:
            return
        self._realtime_stop = asyncio.Event()
        subscriptions = list(self._subscriptions.values())
        self._realtime_task = asyncio.create_task(
            self._realtime.run(subscriptions, self._emit_change, self._realtime_stop),
            name="directus-realtime",
        )

    async def _stop_realtime_locked(self) -> None:
        task = self._realtime_task
        if task is None:
            return
        if self._realtime_stop is not None:
            self._realtime_stop.set()
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task
        self._realtime_task = None
        self._realtime_stop = None

    async def _emit_change(self, event: DirectusChangeEvent) -> None:
        if self._lookup_service is not None:
            self._lookup_service.invalidate_collection(event.collection)
        if event.collection in {"directus_fields", "directus_relations", "directus_permissions"}:
            self._schema_snapshots.clear()
        sink = self._notification_sink
        if sink is not None:
            await sink(event)

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile


def build_directus_service_from_environment(
    task_service: Any = None,
) -> DirectusService | None:
    url = os.environ.get("VIBETABLE_DIRECTUS_URL")
    if not url:
        return None
    project = os.environ.get("VIBETABLE_DIRECTUS_PROJECT", "default")
    manifest_path = Path(os.environ.get("VIBETABLE_DIRECTUS_MANIFEST", DEFAULT_MANIFEST))
    manifest = CapabilityManifest.model_validate_json(manifest_path.read_text(encoding="utf-8"))
    config = DirectusSourceConfig(
        url=url,
        project=project,
        token_ref=f"directus:{project}:active-user",
    )
    transport = StdlibDirectusTransport(config)
    local_app_data = Path(os.environ.get("LOCALAPPDATA", Path.home() / ".vibetable"))
    secrets = DpapiFileSecretStore(local_app_data / "VibeTable" / "credentials")
    auth = DirectusAuthBroker(config, transport, secrets)
    realtime = DirectusRealtimeSupervisor(
        WebsocketsConnector(),
        auth,
        _websocket_url(config.url),
    )
    client = DirectusClient(transport, auth)
    service = DirectusService(manifest, auth, client, realtime)
    service.lookup_service = LookupService(
        transport=transport,
        auth=auth,
        project=project,
        client=client,
    )
    service.lookup_service.set_schema_provider(service._build_schema_snapshot)
    service.relation_schema_service = RelationSchemaService(
        client=client,
        transport=transport,
        auth=auth,
        schema_provider=service._build_schema_snapshot,
        lookup_provider=service.lookup_service.all_definitions,
        lookup_cascade=service.lookup_service.cascade_delete,
    )
    service.relation_service = RelationDataService(
        client=client,
        auth=auth,
        transport=transport,
        profiles=service._profiles,
        resolve_relation=service.resolve_relation,
    )
    service.paste_service = PasteService(
        client=client,
        auth=auth,
        bulk=BulkMutationClient(transport, auth),
        profiles=service._profiles,
        project=project,
        token_store=PasteTokenStore(),
    )
    if task_service is not None:
        service.import_service = ImportService(
            client=client,
            auth=auth,
            bulk=BulkMutationClient(transport, auth),
            profiles=service._profiles,
            resolve_path=task_service.resolve_path,
            consume_grant=task_service.consume_grant,
            relation_provider=DirectusRelationImportProvider(
                client=client,
                transport=transport,
                auth=auth,
                resolve_relation=service.resolve_relation,
            ),
        )
        service.export_service = ExportService(
            client=client,
            auth=auth,
            profiles=service._profiles,
            resolve_path=task_service.resolve_path,
            lookup_provider=LookupExportProvider(
                lookup_service=service.lookup_service,
                schema_provider=service._build_schema_snapshot,
            ),
        )
        service.collaboration_service = CollaborationService(
            client=client,
            auth=auth,
            profiles=service._profiles,
            transport=transport,
        )
        service.history_service = HistoryService(
            client=client,
            auth=auth,
            profiles=service._profiles,
            transport=transport,
            schema_revision=manifest.schema_version,
            proof_secret=os.environ.get("VIBETABLE_HISTORY_PROOF_SECRET"),
        )
        service.document_workspace_service = DocumentWorkspaceService(
            auth=auth,
            profiles=service._profiles,
            transport=transport,
        )
        service.table_admin_service = TableAdminService(
            transport=transport,
            auth=auth,
            # Share the service's mutable runtime profile registry. Calling
            # manifest.by_collection here would create a second dict and make
            # freshly created/reconciled tables invisible to directus.schema.
            profiles=service._profiles,
        )
        service.insights_service = InsightsService(
            client=client,
            auth=auth,
            profiles=service._profiles,
            transport=transport,
        )
        service.file_tools_service = FileToolsService(
            client=client,
            auth=auth,
            profiles=service._profiles,
            transport=transport,
            resolve_path=task_service.resolve_path,
            consume_grant=task_service.consume_grant,
        )
        local_app_data = Path(os.environ.get("LOCALAPPDATA", Path.home() / ".vibetable"))
        service.settings_command_service = SettingsCommandService(
            auth=auth,
            profiles=service._profiles,
            transport=transport,
            device_state_path=local_app_data / "VibeTable" / "device-settings.json",
        )
        plugin_state_dir = local_app_data / "VibeTable" / "plugins"
        plugin_state_dir.mkdir(parents=True, exist_ok=True)
        plugin_store = PluginProjectStore(plugin_state_dir / "plugin-state.db")
        flow_adapter = DirectusFlowAdapter(transport=transport, auth=auth)
        interaction_adapter = DirectusInteractionAdapter(transport=transport, auth=auth)
        bindings = FlowBindingManager(store=plugin_store, directus=flow_adapter)
        registry = PluginRegistry(store=plugin_store, bindings=bindings)
        host_confirmation = HostConfirmationAdapter()
        host_files = HostFileCapabilityAdapter(task_service=task_service)
        runtime = PluginExecutionRuntime(
            registry=registry,
            bindings=bindings,
            tasks=task_service.runtime,
            flow_adapter=flow_adapter,
            worker_adapter=NodePluginWorkerAdapter(
                store=plugin_store,
                profiles=service._profiles,
                client=client,
                file_adapter=host_files,
            ),
            confirmation_adapter=host_confirmation,
            bulk_mutation_adapter=DirectusBulkMutationAdapter(
                transport=transport,
                auth=auth,
                profiles=service._profiles,
            ),
            interaction_adapter=interaction_adapter,
        )
        service._plugin_store = plugin_store
        service._plugin_runtime = runtime
        service._plugin_confirmation = host_confirmation
        service._plugin_files = host_files
        service.plugin_service = PluginPlatformService(
            store=plugin_store,
            registry=registry,
            bindings=bindings,
            directus=flow_adapter,
            runtime=runtime,
            interactions=PluginInteractionBroker(adapter=interaction_adapter),
            package_cache=plugin_state_dir / "packages",
            local_confirmations=host_confirmation,
            local_files=host_files,
        )
    return service


def _canonical_revision(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _decorate_physical_columns(
    *,
    collection: str,
    columns: list[ColumnSchema],
    relations: list[NormalizedRelationDescriptor],
    update_fields: set[str],
    readonly_fields: set[str],
) -> list[ColumnSchema]:
    """Attach stable physical/relation identities to grid columns."""

    relation_ids: dict[str, str] = {}
    prefix = f"{collection}."
    for relation in relations:
        if relation.source_collection != collection:
            continue
        field = relation.field_ref
        if field.startswith(prefix):
            field = field[len(prefix) :]
        if "." not in field:
            relation_ids[field] = relation.relation_id
    return [
        column.model_copy(
            update={
                "field_id": f"{collection}.{column.name}",
                "kind": "relation" if column.name in relation_ids else "scalar",
                "relation_id": relation_ids.get(column.name),
                "lookup_id": None,
                "editable": (column.name in update_fields and column.name not in readonly_fields),
            }
        )
        for column in columns
    ]


def _lookup_columns(definitions: list[LookupDefinition]) -> list[ColumnSchema]:
    """Project saved Lookup definitions as readonly grid columns."""

    return [
        ColumnSchema(
            name=definition.field_key,
            title=definition.display_name,
            field_id=f"{definition.collection}.{definition.field_key}",
            kind="lookup",
            lookup_id=definition.lookup_id,
            data_type=definition.output_type,
            editable=False,
            nullable=True,
            scale=definition.output_scale,
        )
        for definition in sorted(definitions, key=lambda item: item.field_key)
    ]


def _optional_int(value: Any) -> int | None:
    return value if isinstance(value, int) else None


def _websocket_url(url: str) -> str:
    parsed = urlsplit(url)
    scheme = "wss" if parsed.scheme == "https" else "ws"
    path = f"{parsed.path.rstrip('/')}/websocket"
    return urlunsplit((scheme, parsed.netloc, path, "", ""))


def _nested_string(value: Any, *keys: str) -> str | None:
    if not isinstance(value, dict):
        return None
    for key in keys:
        candidate = value.get(key)
        if isinstance(candidate, str) and candidate:
            return candidate
    return None
