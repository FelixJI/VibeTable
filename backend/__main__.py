"""PocketBase-only stdio JSON-RPC composition root."""

from __future__ import annotations

import asyncio
import contextlib
import hashlib
import logging
import os
import sys
import threading
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from backend.adapters.pocketbase.backup import PocketBaseBackupService
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import ProductDataIoRuntime
from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort
from backend.adapters.pocketbase.plugin_mutation import PocketBasePluginMutationAdapter
from backend.adapters.pocketbase.realtime import (
    PocketBaseRealtimeSupervisor,
    ProductEvent,
    StdlibSSEConnector,
)
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.application.document_workspace_service import DocumentWorkspaceService
from backend.application.grid_state_service import GridStateService
from backend.application.identifier_mapping_service import (
    IdentifierManagementService,
    IdentifierRegistry,
)
from backend.application.insights_service import InsightsService
from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry
from backend.application.product_data_service import (
    PRODUCT_PARAM_MODELS,
    PocketBaseProductDataService,
    ProductParams,
)
from backend.application.settings_command_service import SettingsCommandService
from backend.application.system_service import SystemService
from backend.application.task_service import build_task_service
from backend.contracts.backup import (
    CreateBackupParams,
    DeleteBackupParams,
    ListBackupsParams,
    RestoreBackupParams,
)
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    GenerateTemplateParams,
    PreviewImportParams,
)
from backend.contracts.document_workspace import (
    LinkDocumentParams,
    PublishIndexBatchParams,
    ReadDocumentHistoryParams,
    ReadDocumentsParams,
    ReadFolderParams,
    RegisterDocumentParams,
    UnlinkDocumentParams,
)
from backend.contracts.grid_state import GridStateGetParams, GridStateSaveParams
from backend.contracts.paste import ApplyPasteParams, PreviewPasteParams
from backend.contracts.plugin import PluginEventEnvelope
from backend.contracts.plugin_rpc import (
    CommitInstallParams,
    DescribePluginActionParams,
    InspectInstallParams,
    PluginIdentityParams,
    PluginProjectParams,
    PluginTaskParams,
    ResolvePluginFileParams,
    ResolvePluginInteractionParams,
    RollbackPluginParams,
    SetPluginEnabledParams,
    StartPluginActionParams,
    UninstallPluginParams,
    UpgradePluginParams,
)
from backend.contracts.presets_versions_dashboards import (
    CreateVersionParams,
    DashboardWorkspaceParams,
    DeletePresetParams,
    DeleteVersionParams,
    ExecuteDashboardQueryParams,
    ListDashboardsParams,
    ListPresetsParams,
    ListVersionsParams,
    PromoteVersionParams,
    SaveDashboardDraftParams,
    SavePresetParams,
    SaveVersionParams,
    VersionIdParams,
)
from backend.contracts.settings_commands import (
    DeleteShortcutParams,
    LaunchActionParams,
    ListCommandsParams,
    ListShortcutsParams,
    ReadSharedSettingsParams,
    RunCommandParams,
    SaveDeviceSettingsParams,
    SaveShortcutParams,
)
from backend.contracts.system import HandshakeParams
from backend.contracts.table_admin import (
    ListIdentifierMappingsParams,
    ReconcileIdentifierMappingsParams,
    UpdateIdentifierAliasesParams,
)
from backend.contracts.task import (
    CreateTaskParams,
    HostExportTargetParams,
    HostImportSourceParams,
    RequestExportTargetGrantParams,
    RequestImportSourceGrantParams,
    ResolveGrantParams,
    TaskIdParams,
)
from backend.infrastructure.plugin_file_capability import HostFileCapabilityAdapter
from backend.infrastructure.plugin_interaction import HostConfirmationAdapter
from backend.infrastructure.plugin_store import PluginProjectStore
from backend.infrastructure.plugin_worker import NodePluginWorkerAdapter
from backend.rpc.dispatcher import (
    RpcDispatcher,
    register_export_errors,
    register_identifier_errors,
    register_import_errors,
    register_insights_errors,
    register_paste_errors,
    register_path_grant_errors,
    register_plugin_errors,
    register_settings_command_errors,
)
from backend.rpc.framing import MAX_FRAME_BYTES
from backend.rpc.server import RpcServer

_READ_LIMIT = MAX_FRAME_BYTES + 1
logger = logging.getLogger("backend")


class StdoutAsyncWriter:
    def __init__(self, stream: Any) -> None:
        self._stream = stream

    def write(self, data: bytes) -> None:
        self._stream.write(data)
        self._stream.flush()

    async def drain(self) -> None:
        return None


def _configure_logging() -> None:
    logging.basicConfig(
        stream=sys.stderr,
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        force=True,
    )
    app_logger = logging.getLogger("app")
    app_logger.handlers.clear()
    app_logger.propagate = True


def _feed_stdin_to_reader(
    reader: asyncio.StreamReader,
    stdin: Any,
    loop: asyncio.AbstractEventLoop,
) -> None:
    try:
        while line := stdin.readline():
            loop.call_soon_threadsafe(reader.feed_data, line)
    except Exception:
        return
    finally:
        with contextlib.suppress(RuntimeError):
            loop.call_soon_threadsafe(reader.feed_eof)


def _product_runtime() -> tuple[
    PocketBaseProductDataService | None,
    PocketBaseClient | None,
    PocketBaseConfig | None,
]:
    base_url = os.environ.get("VIBETABLE_SIDECAR_URL")
    session_secret = os.environ.get("VIBETABLE_SIDECAR_SESSION_SECRET")
    if not base_url or not session_secret:
        return None, None, None
    config = PocketBaseConfig(base_url=base_url, session_secret=session_secret)
    transport = StdlibPocketBaseTransport(config)
    client = PocketBaseClient(transport=transport, session_secret=session_secret)
    return (
        PocketBaseProductDataService(
            client=client,
            transport=transport,
            session_secret=session_secret,
        ),
        client,
        config,
    )


def _build_document_workspace_service(
    client: PocketBaseClient | None,
) -> DocumentWorkspaceService:
    """Wire workspace record links to the authenticated product data authority."""

    if client is None:
        return DocumentWorkspaceService(collection_catalog={})

    async def load_collection_catalog() -> dict[str, dict[str, Any]]:
        payload = await client.list_tables()
        raw_tables = payload.get("tables")
        if not isinstance(raw_tables, list):
            raise ValueError("PocketBase returned an invalid workspace collection catalog")

        collection_catalog: dict[str, dict[str, Any]] = {}
        for table in raw_tables:
            if not isinstance(table, dict):
                raise ValueError("PocketBase returned an invalid workspace collection catalog")
            table_id = table.get("tableId")
            if not isinstance(table_id, str) or not table_id or table_id != table_id.strip():
                raise ValueError("PocketBase returned an invalid workspace collection id")
            if table_id in collection_catalog:
                raise ValueError("PocketBase returned a duplicate workspace collection id")
            collection_catalog[table_id] = table
        return collection_catalog

    async def record_exists(collection: str, item_id: str) -> bool:
        rows = await client.read_rows(table_id=collection, row_ids=[item_id])
        # A single authoritative primary-key lookup must resolve to exactly one
        # active product row. Empty, duplicate, archived, or malformed results
        # all fail closed.
        return len(rows) == 1

    return DocumentWorkspaceService(
        collection_catalog={},
        collection_catalog_loader=load_collection_catalog,
        record_exists=record_exists,
    )


def _build_pocketbase_product_service() -> PocketBaseProductDataService | None:
    return _product_runtime()[0]


def _register_pocketbase_product_methods(
    dispatcher: RpcDispatcher,
    service: PocketBaseProductDataService,
) -> None:
    from backend.rpc.dispatcher import register_pocketbase_product_errors

    register_pocketbase_product_errors()
    methods = {
        "schema.validate": service.validate_schema,
        "schema.apply": service.apply_schema,
        "schema.delete": service.delete_schema,
        "schema.list": service.list_tables,
        "schema.getTable": service.get_table_schema,
        "schema.describe": service.describe_schema,
        "query.page": service.query_page,
        "query.readRows": service.read_rows,
        "query.validateSnapshot": service.validate_snapshot,
        "mutation.preview": service.preview_mutation,
        "mutation.apply": service.apply_mutation,
        "formula.validate": service.validate_formula,
        "formula.preview": service.preview_formula,
        "file.list": service.list_attachment_refs,
        "file.token": service.create_file_token,
        "file.applyHostChange": service.apply_host_attachment_change,
        "file.saveHostFile": service.save_attachment_to_host,
        "history.read": service.read_history,
        "history.previewRestore": service.preview_history_restore,
        "history.applyRestore": service.apply_history_restore,
        "events.reconcile": service.reconcile,
        "relation.searchTargets": service.search_relation_targets,
        "relation.updateSingle": service.update_single_relation,
        "relation.previewDelta": service.preview_relation_delta,
        "relation.applyDelta": service.apply_relation_delta,
        "table_admin.previewRelationChange": service.preview_relation_change,
        "table_admin.applyRelationChange": service.apply_relation_change,
        "lookup.list": service.list_lookups,
        "lookup.validate": service.validate_lookup,
        "lookup.create": service.create_lookup,
        "lookup.update": service.update_lookup,
        "lookup.delete": service.delete_lookup,
        "lookup.preview": service.preview_lookup,
        "lookup.query": service.query_lookups,
    }
    for method, handler in methods.items():
        dispatcher.register(method, handler, PRODUCT_PARAM_MODELS[method])


def _register_backup_methods(
    dispatcher: RpcDispatcher,
    service: PocketBaseBackupService,
) -> None:
    """Register the fixed backup product operations."""
    from backend.rpc.dispatcher import register_pocketbase_product_errors

    register_pocketbase_product_errors()
    dispatcher.register("backup.list", service.list_backups, ListBackupsParams)
    dispatcher.register("backup.create", service.create_backup, CreateBackupParams)
    dispatcher.register("backup.delete", service.delete_backup, DeleteBackupParams)
    dispatcher.register("backup.restore", service.restore_backup, RestoreBackupParams)


def _configure_pocketbase_data_io(
    dispatcher: RpcDispatcher,
    *,
    client: PocketBaseClient,
    task_service: Any,
) -> ProductDataIoRuntime:
    """Register the product-only paste/import/export vertical slice."""

    register_paste_errors()
    register_import_errors()
    register_export_errors()
    runtime = ProductDataIoRuntime(client=client, task_service=task_service)
    runtime.register_tasks()
    dispatcher.register("table.previewPaste", runtime.preview_paste, PreviewPasteParams)
    dispatcher.register("table.applyPaste", runtime.apply_paste, ApplyPasteParams)
    dispatcher.register("data.previewImport", runtime.preview_import, PreviewImportParams)
    dispatcher.register("data.applyImport", runtime.apply_import, ApplyImportParams)
    dispatcher.register("data.export", runtime.export, ExportParams)
    dispatcher.register(
        "data.generateTemplate",
        runtime.generate_template,
        GenerateTemplateParams,
    )
    return runtime


def _register_document_workspace_methods(
    dispatcher: RpcDispatcher,
    service: DocumentWorkspaceService,
) -> None:
    dispatcher.register("workspace.readFolder", service.read_folder, ReadFolderParams)
    dispatcher.register("workspace.readDocuments", service.read_documents, ReadDocumentsParams)
    dispatcher.register(
        "workspace.registerDocument",
        service.register_document,
        RegisterDocumentParams,
    )
    dispatcher.register(
        "workspace.publishIndexBatch",
        service.publish_index_batch,
        PublishIndexBatchParams,
    )
    dispatcher.register("workspace.linkDocument", service.link_document, LinkDocumentParams)
    dispatcher.register("workspace.unlinkDocument", service.unlink_document, UnlinkDocumentParams)
    dispatcher.register(
        "workspace.readDocumentHistory",
        service.read_document_history,
        ReadDocumentHistoryParams,
    )


def _register_settings_methods(
    dispatcher: RpcDispatcher,
    service: SettingsCommandService,
) -> None:
    register_settings_command_errors()
    dispatcher.register(
        "settings.readDevice",
        lambda _params=None: service.read_device(),
        ListCommandsParams,
    )
    dispatcher.register("settings.saveDevice", service.save_device, SaveDeviceSettingsParams)
    dispatcher.register("settings.readShared", service.read_shared, ReadSharedSettingsParams)
    dispatcher.register(
        "command.list",
        lambda _params=None: service.list_commands(),
        ListCommandsParams,
    )
    dispatcher.register("command.run", service.run_command, RunCommandParams)
    dispatcher.register(
        "shortcut.list",
        lambda _params=None: service.list_shortcuts(),
        ListShortcutsParams,
    )
    dispatcher.register("shortcut.save", service.save_shortcut, SaveShortcutParams)
    dispatcher.register("shortcut.delete", service.delete_shortcut, DeleteShortcutParams)
    dispatcher.register("shortcut.launch", service.launch_action, LaunchActionParams)


def _register_plugin_methods(
    dispatcher: RpcDispatcher,
    service: PluginPlatformService,
) -> None:
    register_plugin_errors()
    dispatcher.register("plugin.listCatalog", service.list_catalog, PluginProjectParams)
    dispatcher.register("plugin.listAudit", service.list_audit, PluginIdentityParams)
    dispatcher.register(
        "plugin.listPendingCleanup",
        service.list_pending_cleanup,
        PluginProjectParams,
    )
    dispatcher.register("plugin.inspectInstall", service.inspect_install, InspectInstallParams)
    dispatcher.register("plugin.commitInstall", service.commit_install, CommitInstallParams)
    dispatcher.register("plugin.setEnabled", service.set_enabled, SetPluginEnabledParams)
    dispatcher.register("plugin.upgrade", service.upgrade, UpgradePluginParams)
    dispatcher.register("plugin.rollback", service.rollback, RollbackPluginParams)
    dispatcher.register("plugin.uninstall", service.uninstall, UninstallPluginParams)
    dispatcher.register(
        "plugin.describeAction",
        service.describe_action,
        DescribePluginActionParams,
    )
    dispatcher.register("plugin.startAction", service.start_action, StartPluginActionParams)
    dispatcher.register(
        "plugin.resolveInteraction",
        service.resolve_interaction,
        ResolvePluginInteractionParams,
    )
    dispatcher.register("plugin.resolveFile", service.resolve_file, ResolvePluginFileParams)
    dispatcher.register("plugin.cancelTask", service.cancel_task, PluginTaskParams)
    dispatcher.register("plugin.getTask", service.get_task, PluginTaskParams)


class _RealtimeRuntime:
    def __init__(self, task: asyncio.Task[None], stop: asyncio.Event) -> None:
        self._task = task
        self._stop = stop

    async def close(self) -> None:
        self._stop.set()
        self._task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await self._task


def _start_realtime(
    server: RpcServer,
    config: PocketBaseConfig | None,
    client: PocketBaseClient | None,
) -> _RealtimeRuntime | None:
    if config is None or client is None:
        return None
    stop = asyncio.Event()
    latest_by_table: dict[str, dict[str, Any]] = {}
    emitted: set[str] = set()

    async def reconcile_cursor_gap() -> None:
        for table_id, previous in tuple(latest_by_table.items()):
            result = await client.reconcile_realtime(
                table_id=table_id,
                schema_revision=str(previous["schemaRevision"]),
                data_revision=str(previous["dataRevision"]),
            )
            action = result["action"]
            if action == "none":
                continue
            identity = (
                f"{table_id}\0{result['currentSchemaRevision']}"
                f"\0{result['currentDataRevision']}\0{action}"
            )
            event_id = "evt_reconcile_" + hashlib.sha256(identity.encode()).hexdigest()[:24]
            if event_id in emitted:
                continue
            emitted.add(event_id)
            await server.notify(
                "data.changed",
                {
                    "contractVersion": "1.0",
                    "topic": "data.changed",
                    "eventId": event_id,
                    "sequence": int(previous["sequence"]) + 1,
                    "occurredAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
                    "schemaRevision": result["currentSchemaRevision"],
                    "dataRevision": result["currentDataRevision"],
                    "changeSetId": None,
                    "tableId": table_id,
                    "recordIds": [],
                    "operation": "schema" if action == "reload-schema" else "update",
                },
            )

    supervisor = PocketBaseRealtimeSupervisor(
        StdlibSSEConnector(config),
        reconcile_cursor_gap=reconcile_cursor_gap,
    )

    async def emit(event: ProductEvent) -> None:
        if event.topic == "data.changed":
            table_id = event.payload.get("tableId")
            if isinstance(table_id, str) and table_id:
                latest_by_table[table_id] = event.payload
        await server.notify(event.topic, event.payload)

    task = asyncio.create_task(supervisor.run(emit, stop), name="product-realtime")
    return _RealtimeRuntime(task, stop)


async def _build_server() -> tuple[
    RpcServer,
    DocumentWorkspaceService,
    PluginPlatformService | None,
    _RealtimeRuntime | None,
]:
    server_ref: RpcServer | None = None

    task_sequence = 0

    async def notify_task_status(status: Any) -> None:
        nonlocal task_sequence
        if server_ref is not None:
            raw = (
                status.model_dump(mode="json", by_alias=True)
                if hasattr(status, "model_dump")
                else dict(status)
            )
            task_sequence += 1
            state = {
                "queued": "pending",
                "running": "running",
                "succeeded": "succeeded",
                "failed": "failed",
                "cancelled": "cancelled",
                "aborted": "failed",
            }.get(str(raw.get("state")), "failed")
            progress = raw.get("progress")
            done = progress.get("done", 0) if isinstance(progress, dict) else 0
            total = progress.get("total", 0) if isinstance(progress, dict) else 0
            ratio = min(1.0, done / total) if isinstance(total, int) and total > 0 else 0.0
            task_id = str(raw.get("taskId", "unknown"))
            kind = str(raw.get("kind", "data.import"))
            task_type = {
                "data.import": "import",
                "data.export": "export",
            }.get(kind, "reconcile")
            identity = f"{task_id}\0{state}\0{done}\0{total}".encode()
            error_message = raw.get("error")
            await server_ref.notify(
                "task.changed",
                {
                    "contractVersion": "1.0",
                    "topic": "task.changed",
                    "eventId": f"evt_task_{hashlib.sha256(identity).hexdigest()[:24]}",
                    "sequence": task_sequence,
                    "occurredAt": datetime.now(UTC).isoformat(),
                    "taskId": task_id,
                    "taskType": task_type,
                    "state": state,
                    "progress": ratio,
                    "cursor": None,
                    "error": (
                        {
                            "contractVersion": "1.0",
                            "code": "task.failed",
                            "path": None,
                            "message": str(error_message),
                            "details": {},
                            "retryable": False,
                        }
                        if error_message
                        else None
                    ),
                },
            )

    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader(limit=_READ_LIMIT, loop=loop)
    threading.Thread(
        target=_feed_stdin_to_reader,
        args=(reader, sys.stdin.buffer, loop),
        name="rpc-stdin-feeder",
        daemon=True,
    ).start()
    dispatcher = RpcDispatcher()
    dispatcher.register(
        "system.handshake",
        SystemService(lambda: dispatcher.registered_methods).handshake,
        HandshakeParams,
    )
    grid = GridStateService()
    dispatcher.register("gridState.get", grid.get, GridStateGetParams)
    dispatcher.register("gridState.save", grid.save, GridStateSaveParams)

    register_path_grant_errors()
    task_service = build_task_service(notification_sink=notify_task_status)
    dispatcher.register("task.create", task_service.create_task, CreateTaskParams)
    dispatcher.register("task.cancel", task_service.cancel_task, TaskIdParams)
    dispatcher.register("task.status", task_service.status_task, TaskIdParams)
    dispatcher.register(
        "path.requestImportSource",
        task_service.register_import_source,
        RequestImportSourceGrantParams,
    )
    dispatcher.register(
        "path.requestExportTarget",
        task_service.register_export_target,
        RequestExportTargetGrantParams,
    )
    dispatcher.register(
        "path.registerImportSource",
        task_service.register_host_import_source,
        HostImportSourceParams,
    )
    dispatcher.register(
        "path.registerExportTarget",
        task_service.register_host_export_target,
        HostExportTargetParams,
    )
    dispatcher.register("path.resolveGrant", task_service.resolve_grant, ResolveGrantParams)

    product_service, client, config = _product_runtime()
    workspace = _build_document_workspace_service(client)
    _register_document_workspace_methods(dispatcher, workspace)

    plugin_service: PluginPlatformService | None = None
    if product_service is not None and client is not None:
        _register_pocketbase_product_methods(dispatcher, product_service)
        _register_backup_methods(dispatcher, PocketBaseBackupService(client))
        data_io = _configure_pocketbase_data_io(
            dispatcher,
            client=client,
            task_service=task_service,
        )
        metadata = PocketBaseInternalMetadataPort(client=client, schema_revisions={})
        state_root = Path(
            os.environ.get(
                "VIBETABLE_STATE_DIR",
                str(Path.home() / ".vibetable"),
            )
        )

        async def execute_export_command(
            raw_params: dict[str, Any],
            grant_id: str,
        ) -> dict[str, Any]:
            params = ExportParams.model_validate({**raw_params, "grantId": grant_id})
            result = await data_io.export(params)
            return result.model_dump(by_alias=True, mode="json")

        settings = SettingsCommandService(
            metadata_port=metadata,
            device_state_path=state_root / "device-settings.json",
            grant_authority=task_service.grants,
            command_executors={"export.query": execute_export_command},
        )
        _register_settings_methods(dispatcher, settings)
        insights = InsightsService(metadata_port=metadata, query_port=client)
        register_insights_errors()

        # Insights is intentionally exposed under product-owned method names.
        async def read_dashboard_workspace(
            params: DashboardWorkspaceParams,
        ) -> Any:
            return await insights.read_dashboard_workspace(params.dashboard_id)

        dispatcher.register(
            "insights.listDashboards",
            insights.list_dashboards,
            ListDashboardsParams,
        )
        dispatcher.register(
            "insights.readDashboardWorkspace",
            read_dashboard_workspace,
            DashboardWorkspaceParams,
        )
        dispatcher.register(
            "insights.saveDashboardDraft",
            insights.save_dashboard_draft,
            SaveDashboardDraftParams,
        )
        dispatcher.register(
            "insights.deleteDashboardWorkspace",
            insights.delete_dashboard_workspace,
            DashboardWorkspaceParams,
        )
        dispatcher.register(
            "insights.executeDashboardQuery",
            insights.execute_dashboard_query,
            ExecuteDashboardQueryParams,
        )
        dispatcher.register(
            "insights.dashboardQueryLimits",
            insights.dashboard_query_limits,
            ProductParams,
        )
        dispatcher.register(
            "insights.panelManifest",
            insights.panel_manifest,
            ProductParams,
        )
        dispatcher.register("preset.list", insights.list_presets, ListPresetsParams)
        dispatcher.register("preset.save", insights.save_preset, SavePresetParams)
        dispatcher.register("preset.delete", insights.delete_preset, DeletePresetParams)
        dispatcher.register("version.list", insights.list_versions, ListVersionsParams)
        dispatcher.register("version.create", insights.create_version, CreateVersionParams)
        dispatcher.register("version.save", insights.save_version, SaveVersionParams)
        dispatcher.register("version.compare", insights.compare_version, VersionIdParams)
        dispatcher.register("version.promote", insights.promote_version, PromoteVersionParams)
        dispatcher.register("version.delete", insights.delete_version, DeleteVersionParams)
        identifier = IdentifierRegistry(metadata)
        identifier_service = IdentifierManagementService(
            registry=identifier,
            schema_port=client,
        )
        register_identifier_errors()
        dispatcher.register(
            "identifier.list",
            identifier_service.list,
            ListIdentifierMappingsParams,
        )
        dispatcher.register(
            "identifier.updateAliases",
            identifier_service.update_aliases,
            UpdateIdentifierAliasesParams,
        )
        dispatcher.register(
            "identifier.reconcile",
            identifier_service.reconcile,
            ReconcileIdentifierMappingsParams,
        )

        store = PluginProjectStore(state_root / "plugins.db")
        registry = PluginRegistry(store=store)
        confirmation = HostConfirmationAdapter()
        file_capability = HostFileCapabilityAdapter(task_service=task_service)
        worker = NodePluginWorkerAdapter(
            store=store,
            profiles={},
            client=client,
            file_adapter=file_capability,
        )
        mutation = PocketBasePluginMutationAdapter(
            client=client,
            schema_revisions={},
            writable_fields={},
        )
        runtime = PluginExecutionRuntime(
            registry=registry,
            worker_adapter=worker,
            confirmation_adapter=confirmation,
            mutation_adapter=mutation,
        )
        plugin_service = PluginPlatformService(
            registry=registry,
            runtime=runtime,
            store=store,
            confirmation_adapter=confirmation,
            file_adapter=file_capability,
        )
        _register_plugin_methods(dispatcher, plugin_service)

    server = RpcServer(reader, StdoutAsyncWriter(sys.stdout.buffer), dispatcher)
    server_ref = server
    if plugin_service is not None:

        async def notify_plugin(event: PluginEventEnvelope) -> None:
            method = {
                "plugin.catalog.changed": "plugin.catalogChanged",
                "plugin.task.changed": "plugin.taskChanged",
                "plugin.interaction.requested": "plugin.interactionRequested",
                "plugin.file.requested": "plugin.fileRequested",
            }.get(event.event_type)
            if method is not None:
                await server.notify(method, event)

        plugin_service.set_notification_sink(notify_plugin)
    return server, workspace, plugin_service, _start_realtime(server, config, client)


async def _main() -> None:
    _configure_logging()
    workspace: DocumentWorkspaceService | None = None
    plugin_service: PluginPlatformService | None = None
    realtime: _RealtimeRuntime | None = None
    try:
        server, workspace, plugin_service, realtime = await _build_server()
        await server.serve()
    finally:
        if realtime is not None:
            await realtime.close()
        if plugin_service is not None:
            await plugin_service.close()
        if workspace is not None:
            workspace.close()


if __name__ == "__main__":
    asyncio.run(_main())
