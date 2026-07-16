"""Stdio entrypoint: ``python -m backend``.

Boots the JSON-RPC server over the process stdin/stdout. Diagnostics and
logging go to **stderr**; stdout carries only framed JSON-RPC responses and
notifications (the protocol stream the host process reads).

Usage (host side)::

    echo '{"jsonrpc":"2.0",...}' | python -m backend

Transport note
--------------
On Windows, ``asyncio.get_event_loop().connect_read_pipe(...)`` registers the
stdin handle with the IOCP completion port. When stdin is a redirected pipe
(as it is for any host process spawning us via ``subprocess``/``CreateProcess``
with pipe redirection), that registration fails with ``WinError 6`` ("the
handle is invalid"). To stay portable across Windows-redirected-pipe and
POSIX, we feed an ``asyncio.StreamReader`` (with the requested
``MAX_FRAME_BYTES + 1`` read limit) from a daemon thread that performs
blocking ``readline`` calls on ``sys.stdin.buffer``. This preserves the brief's
async framing contract while sidestepping the IOCP limitation. The write side
is a plain ``AsyncWriter`` adapter over ``sys.stdout.buffer``.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import sys
import threading
from typing import Any

from backend.application.directus_service import build_directus_service_from_environment
from backend.application.grid_state_service import GridStateService
from backend.application.system_service import SystemService
from backend.application.task_service import build_task_service
from backend.contracts.collaboration import (
    ApplyRevertParams,
    CreateCommentParams,
    DeleteCommentParams,
    NotificationIdParams,
    PreviewRevertParams,
    ReadActivityParams,
    ReadCommentsParams,
    ReadNotificationsParams,
    SearchMentionsParams,
    UpdateCommentParams,
)
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    GenerateTemplateParams,
    PreviewImportParams,
)
from backend.contracts.directus import (
    DirectusCollectionParams,
    DirectusCreateParams,
    DirectusEmptyParams,
    DirectusItemParams,
    DirectusLoginParams,
    DirectusReadParams,
    DirectusSubscribeParams,
    DirectusUnsubscribeParams,
    DirectusUpdateParams,
)
from backend.contracts.grid_state import (
    GridStateGetParams,
    GridStateSaveParams,
)
from backend.contracts.history import (
    ApplyRestoreParams as HistoryApplyRestoreParams,
)
from backend.contracts.history import (
    PreviewRestoreParams as HistoryPreviewRestoreParams,
)
from backend.contracts.history import (
    ReadChangeSetsParams,
)
from backend.contracts.paste import ApplyPasteParams, PreviewPasteParams
from backend.contracts.relation import RelationProjectionParams
from backend.contracts.system import HandshakeParams
from backend.contracts.task import (
    CreateTaskParams,
    RequestExportTargetGrantParams,
    RequestImportSourceGrantParams,
    ResolveGrantParams,
    TaskIdParams,
)
from backend.rpc.dispatcher import (
    RpcDispatcher,
    register_collaboration_errors,
    register_directus_errors,
    register_file_tools_errors,
    register_import_errors,
    register_insights_errors,
    register_paste_errors,
    register_path_grant_errors,
    register_settings_command_errors,
    register_table_admin_errors,
)
from backend.rpc.framing import MAX_FRAME_BYTES, AsyncWriter
from backend.rpc.server import RpcServer

#: Read limit lets ``readuntil`` accept a frame whose encoded size is exactly
#: ``MAX_FRAME_BYTES`` (the trailing newline pushes it one byte over).
_READ_LIMIT = MAX_FRAME_BYTES + 1

logger = logging.getLogger("backend")


class StdoutAsyncWriter:
    """Adapter that lets the framing layer treat ``sys.stdout.buffer`` as an
    ``AsyncWriter``.

    ``sys.stdout.buffer`` is a synchronous blocking stream; there is no
    backpressure to ``drain``, so ``drain`` is a no-op coroutine. ``flush``
    is called eagerly so a host process reading line-by-line sees each frame
    promptly even if Python's stdout is block-buffered.
    """

    def __init__(self, stream: Any) -> None:
        self._stream = stream

    def write(self, data: bytes) -> None:
        self._stream.write(data)
        self._stream.flush()

    async def drain(self) -> None:
        return None


def _configure_logging() -> None:
    """Route all diagnostics to stderr; never to stdout."""
    logging.basicConfig(
        stream=sys.stderr,
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        force=True,
    )

    # Legacy services obtain loggers through shared.helpers.AppLogger, which
    # installs its own handler on the named ``app`` logger and points that
    # handler at stdout. ``basicConfig(force=True)`` only replaces handlers on
    # the root logger, so the first database INFO line otherwise corrupts the
    # JSON-RPC stdout stream. In the backend process, remove the legacy handler
    # and let the ``app.*`` hierarchy propagate to the stderr-only root.
    app_logger = logging.getLogger("app")
    app_logger.handlers.clear()
    app_logger.propagate = True


def _feed_stdin_to_reader(
    reader: asyncio.StreamReader,
    stdin: Any,
    loop: asyncio.AbstractEventLoop,
) -> None:
    """Daemon-thread loop: blocking-read lines from ``stdin.buffer`` and push
    them into the asyncio ``reader``.

    Each line is pushed verbatim (including the trailing ``\\n``) so the
    framing layer's ``readuntil(b"\\n")`` sees the same bytes it would on a
    real pipe transport. On EOF the loop pushes EOF into the reader and exits.
    """
    try:
        while True:
            line = stdin.readline()
            if not line:
                break
            loop.call_soon_threadsafe(reader.feed_data, line)
    except Exception:
        # The reader is gone (loop closed) or stdin raised; either way, stop.
        return
    finally:
        with contextlib.suppress(RuntimeError):
            # Loop already closed during shutdown — nothing more to do.
            loop.call_soon_threadsafe(reader.feed_eof)


def _register_insights_methods(dispatcher: RpcDispatcher, service: Any) -> None:
    """Register the C2 insights methods (Presets/Dashboards).

    G1.4: Content Versions RPCs (listVersions/createVersion/saveVersion/
    compareVersion/promoteVersion/deleteVersion) are removed from the runtime
    surface. The DTO classes remain in ``presets_versions_dashboards.py`` for
    backward-compatible contract deserialization, but they are no longer
    registered as active RPCs. The G1 capability declares
    ``content_versions`` as disabled.
    """
    from backend.contracts.presets_versions_dashboards import (
        DashboardIdParams,
        DeletePresetParams,
        ListDashboardsParams,
        ListPresetsParams,
        PanelIdParams,
        SaveDashboardParams,
        SavePanelParams,
        SavePresetParams,
    )

    # Presets (Task 4).
    dispatcher.register("directus.listPresets", service.list_presets, ListPresetsParams)
    dispatcher.register("directus.savePreset", service.save_preset, SavePresetParams)
    dispatcher.register("directus.deletePreset", service.delete_preset, DeletePresetParams)
    # G1.4: Content Versions (Task 5) RPCs removed — superseded by
    # history.readChangeSets / history.previewRestore / history.applyRestore.
    # Dashboards / Panels (Tasks 6-7).
    dispatcher.register("directus.listDashboards", service.list_dashboards, ListDashboardsParams)
    dispatcher.register("directus.readDashboard", service.read_dashboard, DashboardIdParams)
    dispatcher.register("directus.saveDashboard", service.save_dashboard, SaveDashboardParams)
    dispatcher.register("directus.deleteDashboard", service.delete_dashboard, DashboardIdParams)
    dispatcher.register("directus.savePanel", service.save_panel, SavePanelParams)
    dispatcher.register("directus.deletePanel", service.delete_panel, PanelIdParams)
    dispatcher.register(
        "directus.panelManifest",
        lambda _params=None: service.panel_manifest(),
        DirectusEmptyParams,
    )


def _register_file_tools_methods(dispatcher: RpcDispatcher, service: Any) -> None:
    """Register the D1 file-tools methods (Files/journal)."""
    from backend.contracts.file_tools import (
        DeleteFileParams,
        JournalIdParams,
        ListJournalParams,
        PresetPreviewParams,
        ReadFilesParams,
        ResolveJournalParams,
        UnlinkFileParams,
        UploadFileParams,
    )

    # Directus Files workspace (Task 3).
    dispatcher.register("directus.readFiles", service.read_files, ReadFilesParams)
    dispatcher.register("directus.uploadFile", service.upload_file, UploadFileParams)
    dispatcher.register("directus.unlinkFile", service.unlink_file, UnlinkFileParams)
    dispatcher.register("directus.deleteFile", service.delete_file, DeleteFileParams)
    dispatcher.register("directus.presetPreview", service.preset_preview, PresetPreviewParams)
    # Operation journal (Task 2).
    dispatcher.register("file.listJournal", service.list_journal, ListJournalParams)
    dispatcher.register("file.resolveJournal", service.resolve_journal, ResolveJournalParams)
    dispatcher.register("file.discardJournal", service.discard_journal, JournalIdParams)


def _register_settings_command_methods(dispatcher: RpcDispatcher, service: Any) -> None:
    """Register the D2 settings/flows/commands/shortcuts methods."""
    from backend.contracts.settings_commands import (
        DeleteShortcutParams,
        InvokeFlowParams,
        LaunchActionParams,
        ListApprovedFlowsParams,
        ListCommandsParams,
        ListShortcutsParams,
        ReadSharedSettingsParams,
        RunCommandParams,
        SaveDeviceSettingsParams,
        SaveShortcutParams,
    )

    # Settings (D2.1).
    dispatcher.register(
        "settings.readDevice", lambda _p=None: service.read_device(), DirectusEmptyParams
    )
    dispatcher.register("settings.saveDevice", service.save_device, SaveDeviceSettingsParams)
    dispatcher.register("settings.readShared", service.read_shared, ReadSharedSettingsParams)
    # Flows (D2.2).
    dispatcher.register(
        "flow.listApproved", lambda _p=None: service.list_approved_flows(), ListApprovedFlowsParams
    )
    dispatcher.register("flow.invoke", service.invoke_flow, InvokeFlowParams)
    # Commands (D2.3).
    dispatcher.register("command.list", lambda _p=None: service.list_commands(), ListCommandsParams)
    dispatcher.register("command.run", service.run_command, RunCommandParams)
    # Shortcuts (D2.4).
    dispatcher.register(
        "shortcut.list", lambda _p=None: service.list_shortcuts(), ListShortcutsParams
    )
    dispatcher.register("shortcut.save", service.save_shortcut, SaveShortcutParams)
    dispatcher.register("shortcut.delete", service.delete_shortcut, DeleteShortcutParams)
    dispatcher.register("shortcut.launch", service.launch_action, LaunchActionParams)


def _register_table_admin_methods(dispatcher: RpcDispatcher, service: Any) -> None:
    """Register the Phase 4 table_admin methods (runtime create/delete collections)."""
    from backend.contracts.table_admin import (
        CreateTableParams,
        DeleteTableParams,
    )

    dispatcher.register("table_admin.createTable", service.create_table, CreateTableParams)
    dispatcher.register("table_admin.deleteTable", service.delete_table, DeleteTableParams)


async def _build_server() -> tuple[RpcServer, Any | None]:
    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader(limit=_READ_LIMIT, loop=loop)
    # Feed the reader from a daemon thread instead of connect_read_pipe: see
    # the module docstring for the Windows IOCP rationale.
    feeder = threading.Thread(
        target=_feed_stdin_to_reader,
        args=(reader, sys.stdin.buffer, loop),
        name="rpc-stdin-feeder",
        daemon=True,
    )
    feeder.start()

    writer: AsyncWriter = StdoutAsyncWriter(sys.stdout.buffer)

    dispatcher = RpcDispatcher()
    system_service = SystemService(lambda: dispatcher.registered_methods)
    dispatcher.register("system.handshake", system_service.handshake, HandshakeParams)

    # F stage: the legacy SQLite business path (database.open, table.list/read,
    # table.getEditSchema, table.updateCell/insertRow/deleteRows/readRows,
    # table.validateSnapshot) has been removed. Business reads/writes now go
    # through the Directus data plane (directus.read/create/update/...).

    # B3 Task 3: durable per-table grid state (local user-state DB).
    grid_state_service = GridStateService()
    dispatcher.register("gridState.get", grid_state_service.get, GridStateGetParams)
    dispatcher.register("gridState.save", grid_state_service.save, GridStateSaveParams)

    # C1: task runtime + session path grants (generic infrastructure; the file
    # picker runs in WPF and registers grants host-side).
    register_path_grant_errors()
    server_ref: RpcServer | None = None

    async def notify_task_status(status: Any) -> None:
        if server_ref is not None:
            await server_ref.notify("task.status", status)

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
    dispatcher.register("path.resolveGrant", task_service.resolve_grant, ResolveGrantParams)

    # B4: Directus-first methods are enabled only when a non-secret project URL
    # is configured. Tokens remain entirely inside the Python broker.
    directus_service = build_directus_service_from_environment(task_service=task_service)
    if directus_service is not None:
        register_directus_errors()
        dispatcher.register("directus.login", directus_service.login, DirectusLoginParams)
        dispatcher.register("directus.refresh", directus_service.refresh, DirectusEmptyParams)
        dispatcher.register("directus.logout", directus_service.logout, DirectusEmptyParams)
        dispatcher.register("directus.status", directus_service.status, DirectusEmptyParams)
        dispatcher.register(
            "directus.serverInfo", directus_service.server_info, DirectusEmptyParams
        )
        dispatcher.register(
            "directus.currentUser", directus_service.current_user, DirectusEmptyParams
        )
        dispatcher.register(
            "directus.collections", directus_service.list_collections, DirectusEmptyParams
        )
        dispatcher.register("directus.schema", directus_service.schema, DirectusCollectionParams)
        dispatcher.register("directus.read", directus_service.read, DirectusReadParams)
        dispatcher.register("directus.create", directus_service.create, DirectusCreateParams)
        dispatcher.register("directus.update", directus_service.update, DirectusUpdateParams)
        dispatcher.register("directus.archive", directus_service.archive, DirectusItemParams)
        dispatcher.register("directus.restore", directus_service.restore, DirectusItemParams)
        dispatcher.register("directus.delete", directus_service.delete, DirectusItemParams)
        dispatcher.register(
            "directus.subscribe", directus_service.subscribe, DirectusSubscribeParams
        )
        dispatcher.register(
            "directus.unsubscribe", directus_service.unsubscribe, DirectusUnsubscribeParams
        )
        # B2: multi-row paste (transparent preview + atomic batch write).
        paste_service = directus_service.paste_service
        if paste_service is not None:
            register_paste_errors()
            dispatcher.register("table.previewPaste", paste_service.preview, PreviewPasteParams)
            dispatcher.register("table.applyPaste", paste_service.apply, ApplyPasteParams)
        # C1: Directus-aware import (preview + chunked apply).
        import_service = directus_service.import_service
        if import_service is not None:
            register_import_errors()
            dispatcher.register("data.previewImport", import_service.preview, PreviewImportParams)
            dispatcher.register("data.applyImport", import_service.apply, ApplyImportParams)
        # C1: query-based export + template generation.
        export_service = directus_service.export_service
        if export_service is not None:
            dispatcher.register("data.export", export_service.export, ExportParams)
            dispatcher.register(
                "data.generateTemplate",
                export_service.generate_template,
                GenerateTemplateParams,
            )
        # C1 Task 5: relation workspace (declared relations only, no generic join).
        dispatcher.register(
            "data.relationProjection",
            directus_service.relation_projection,
            RelationProjectionParams,
        )
        # C2 Tasks 1-3: Activity/Revisions/Revert, Comments, Notifications.
        collaboration_service = directus_service.collaboration_service
        if collaboration_service is not None:
            register_collaboration_errors()
            dispatcher.register(
                "directus.readActivity",
                collaboration_service.read_activity,
                ReadActivityParams,
            )
            dispatcher.register(
                "directus.previewRevert",
                collaboration_service.preview_revert,
                PreviewRevertParams,
            )
            dispatcher.register(
                "directus.applyRevert",
                collaboration_service.apply_revert,
                ApplyRevertParams,
            )
            dispatcher.register(
                "directus.readComments",
                collaboration_service.read_comments,
                ReadCommentsParams,
            )
            dispatcher.register(
                "directus.createComment",
                collaboration_service.create_comment,
                CreateCommentParams,
            )
            dispatcher.register(
                "directus.updateComment",
                collaboration_service.update_comment,
                UpdateCommentParams,
            )
            dispatcher.register(
                "directus.deleteComment",
                collaboration_service.delete_comment,
                DeleteCommentParams,
            )
            dispatcher.register(
                "directus.searchMentions",
                collaboration_service.search_mentions,
                SearchMentionsParams,
            )
            dispatcher.register(
                "directus.readNotifications",
                collaboration_service.read_notifications,
                ReadNotificationsParams,
            )
            dispatcher.register(
                "directus.archiveNotification",
                collaboration_service.archive_notification,
                NotificationIdParams,
            )
            dispatcher.register(
                "directus.deleteNotification",
                collaboration_service.delete_notification,
                NotificationIdParams,
            )
        # C2 Tasks 4-7: Presets, Content Versions, Dashboards/Panels, filter.
        insights_service = directus_service.insights_service
        if insights_service is not None:
            register_insights_errors()
            _register_insights_methods(dispatcher, insights_service)
        # D1: file tools (Directus Files, operation journal, content replace).
        file_tools_service = directus_service.file_tools_service
        if file_tools_service is not None:
            register_file_tools_errors()
            _register_file_tools_methods(dispatcher, file_tools_service)
        # D2: settings, Flows, commands, shortcuts.
        settings_command_service = directus_service.settings_command_service
        if settings_command_service is not None:
            register_settings_command_errors()
            _register_settings_command_methods(dispatcher, settings_command_service)
        # G1: full-field history ChangeSets + safe restore.
        history_service = directus_service.history_service
        if history_service is not None:
            dispatcher.register(
                "history.readChangeSets",
                history_service.read_change_sets,
                ReadChangeSetsParams,
            )
            dispatcher.register(
                "history.previewRestore",
                history_service.preview_restore,
                HistoryPreviewRestoreParams,
            )
            dispatcher.register(
                "history.applyRestore",
                history_service.apply_restore,
                HistoryApplyRestoreParams,
            )
        # G3: document workspace metadata RPC.
        document_workspace_service = directus_service.document_workspace_service
        if document_workspace_service is not None:
            from backend.contracts.document_workspace import (
                LinkDocumentParams,
                PublishIndexBatchParams,
                ReadDocumentHistoryParams,
                ReadFolderParams,
                UnlinkDocumentParams,
            )

            dispatcher.register(
                "workspace.readFolder",
                document_workspace_service.read_folder,
                ReadFolderParams,
            )
            dispatcher.register(
                "workspace.publishIndexBatch",
                document_workspace_service.publish_index_batch,
                PublishIndexBatchParams,
            )
            dispatcher.register(
                "workspace.linkDocument",
                document_workspace_service.link_document,
                LinkDocumentParams,
            )
            dispatcher.register(
                "workspace.unlinkDocument",
                document_workspace_service.unlink_document,
                UnlinkDocumentParams,
            )
            dispatcher.register(
                "workspace.readDocumentHistory",
                document_workspace_service.read_document_history,
                ReadDocumentHistoryParams,
            )
        # Phase 4: runtime table admin (create/delete Directus collections).
        register_table_admin_errors()
        table_admin_service = directus_service.table_admin_service
        if table_admin_service is not None:
            _register_table_admin_methods(dispatcher, table_admin_service)
    server = RpcServer(reader, writer, dispatcher)
    server_ref = server
    if directus_service is not None:
        directus_service.set_notification_sink(
            lambda event: server.notify("directus.changed", event)
        )
    return server, directus_service


async def _main() -> None:
    _configure_logging()
    logger.info("rpc server starting (protocol=%s)", "1.0")
    directus_service: Any | None = None
    try:
        server, directus_service = await _build_server()
        await server.serve()
    finally:
        if directus_service is not None:
            await directus_service.close()
        # F stage: the aiosqlite loop-connection cleanup has been removed (no
        # business SQLite path remains). Directus cleanup is handled above.


def main() -> None:
    # The host now drives the local Directus runtime directly (DirectusSupervisor
    # + DirectusPackageManager + DirectusSchemaBootstrapper in C#), so the BFF no
    # longer has a --local-directus-runner sub-mode. The packaged backend is a
    # pure JSON-RPC BFF.
    if sys.platform == "win32":
        # ProactorEventLoop is the default on Windows since 3.8 and is what we
        # want for asyncio.run; set the policy explicitly so a host process
        # that overrides the policy does not change our behavior.
        asyncio.set_event_loop_policy(asyncio.WindowsProactorEventLoopPolicy())
    asyncio.run(_main())


if __name__ == "__main__":
    main()
