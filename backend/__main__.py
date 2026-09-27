"""PocketBase-only stdio JSON-RPC composition root."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import sys
import threading
from pathlib import Path
from typing import Any

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import ProductDataIoRuntime
from backend.adapters.pocketbase.plugin_mutation import PocketBasePluginMutationAdapter
from backend.adapters.pocketbase.plugin_store import PocketBasePluginStore
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.application.host_files import HostFiles
from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry
from backend.application.system_service import SystemService
from backend.application.task_service import build_task_service
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    GenerateTemplateParams,
    PreviewImportParams,
)
from backend.contracts.plugin import PluginEventEnvelope
from backend.contracts.plugin_rpc import (
    CancelInstallParams,
    CommitInstallParams,
    DescribePluginActionParams,
    InspectInstallParams,
    PluginTaskParams,
    ResolvePluginFileParams,
    ResolvePluginInteractionParams,
    RollbackPluginParams,
    StartPluginActionParams,
    UninstallPluginParams,
    UpgradePluginParams,
)
from backend.contracts.system import HandshakeParams
from backend.contracts.task import (
    ResolveGrantParams,
    StartTaskExecutionParams,
    TaskIdParams,
)
from backend.infrastructure.diagnostic_logging import configure_diagnostic_logging
from backend.infrastructure.plugin_file_capability import HostFileCapabilityAdapter
from backend.infrastructure.plugin_interaction import HostConfirmationAdapter
from backend.infrastructure.plugin_package_lifecycle import LocalPluginPackageLifecycle
from backend.infrastructure.plugin_worker import NodePluginWorkerAdapter
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import ErrorDomain, register_application_errors
from backend.rpc.framing import MAX_FRAME_BYTES
from backend.rpc.product_errors import register_product_rpc_errors
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
    configure_diagnostic_logging(sys.stderr)
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


def _product_runtime() -> PocketBaseClient | None:
    base_url = os.environ.get("VIBETABLE_SIDECAR_URL")
    session_secret = os.environ.get("VIBETABLE_SIDECAR_SESSION_SECRET")
    if not base_url or not session_secret:
        return None
    config = PocketBaseConfig(base_url=base_url, session_secret=session_secret)
    return PocketBaseClient(
        transport=StdlibPocketBaseTransport(config), session_secret=session_secret
    )


def _configure_pocketbase_data_io(
    dispatcher: RpcDispatcher,
    *,
    client: PocketBaseClient,
    task_service: Any,
) -> ProductDataIoRuntime:
    """Register the remaining product import/export worker paths."""

    register_product_rpc_errors()
    register_application_errors(ErrorDomain.PASTE, ErrorDomain.IMPORT, ErrorDomain.EXPORT)
    runtime = ProductDataIoRuntime(client=client, task_service=task_service)
    runtime.register_tasks()
    dispatcher.register("data.previewImport", runtime.preview_import, PreviewImportParams)
    dispatcher.register("data.applyImport", runtime.apply_import, ApplyImportParams)
    dispatcher.register("data.export", runtime.export, ExportParams)
    dispatcher.register(
        "data.generateTemplate",
        runtime.generate_template,
        GenerateTemplateParams,
    )
    return runtime


def _register_plugin_methods(
    dispatcher: RpcDispatcher,
    service: PluginPlatformService,
) -> None:
    register_application_errors(ErrorDomain.PLUGIN)
    # plugin.listCatalog/listAudit/setEnabled and the pending-cleanup
    # projection are answered by the Go plugin catalog through the Host;
    # Python only keeps the closed install/execution surface.
    dispatcher.register("plugin.inspectInstall", service.inspect_install, InspectInstallParams)
    dispatcher.register("plugin.commitInstall", service.commit_install, CommitInstallParams)
    dispatcher.register("plugin.cancelInstall", service.cancel_install, CancelInstallParams)
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
    # plugin.cancelTask stays the closed host-only execution-cancel entry: the
    # public task snapshot/query authority lives in the WPF host registry and
    # Python is never asked for public task state.
    dispatcher.register("plugin.cancelTask", service.cancel_task, PluginTaskParams)


async def _build_server() -> tuple[
    RpcServer,
    PluginPlatformService | None,
]:
    server_ref: RpcServer | None = None

    async def notify_task_status(status: Any) -> None:
        if server_ref is not None:
            await server_ref.notify(
                "task.executionReport",
                status.model_dump(mode="json", by_alias=True),
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
    register_application_errors(ErrorDomain.PATH_GRANT)

    async def call_host_file(action: str, params: dict[str, Any]) -> dict[str, Any]:
        if server_ref is None:
            raise RuntimeError("Host file channel is not connected")
        return await server_ref.call_host_file(action, params)

    task_service = build_task_service(
        files=HostFiles(call_host_file), notification_sink=notify_task_status
    )
    dispatcher.register(
        "task.startExecution", task_service.start_execution, StartTaskExecutionParams
    )
    dispatcher.register("task.cancelExecution", task_service.cancel_execution, TaskIdParams)
    dispatcher.register("task.settleExport", task_service.settle_export, ResolveGrantParams)

    client = _product_runtime()
    plugin_service: PluginPlatformService | None = None
    if client is not None:
        _configure_pocketbase_data_io(
            dispatcher,
            client=client,
            task_service=task_service,
        )
        state_root = Path(
            os.environ.get(
                "VIBETABLE_STATE_DIR",
                str(Path.home() / ".vibetable"),
            )
        )

        store = PocketBasePluginStore(
            client=client,
            package_cache=state_root / "plugin-packages",
        )
        package_lifecycle = LocalPluginPackageLifecycle(store.package_cache)
        registry = PluginRegistry(store=store)
        confirmation = HostConfirmationAdapter()
        file_capability = HostFileCapabilityAdapter(files=task_service.files)
        worker = NodePluginWorkerAdapter(
            store=store,
            profiles={},
            client=client,
            package_lifecycle=package_lifecycle,
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
            package_lifecycle=package_lifecycle,
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
    return server, plugin_service


async def _main() -> None:
    _configure_logging()
    plugin_service: PluginPlatformService | None = None
    try:
        server, plugin_service = await _build_server()
        await server.serve()
    finally:
        if plugin_service is not None:
            await plugin_service.close()


if __name__ == "__main__":
    asyncio.run(_main())
