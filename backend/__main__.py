"""PocketBase-only stdio JSON-RPC composition root."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import sys
import threading
from pathlib import Path
from typing import Any, NoReturn

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import ProductDataIoRuntime
from backend.adapters.pocketbase.plugin_store import PocketBasePluginStore
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.application.host_files import HostFiles
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.application.system_service import SystemService
from backend.application.task_service import build_task_service
from backend.contracts.data_io import (
    ApplyImportParams,
    ExportParams,
    GenerateTemplateParams,
    PreviewImportParams,
)
from backend.contracts.plugin_rpc import (
    CommitInstallExecutionParams,
    InspectInstallExecutionParams,
    RetiredPluginExecutionParams,
    RollbackPluginParams,
    SetPluginEnabledExecutionParams,
    UninstallPluginParams,
    UpgradePluginExecutionParams,
)
from backend.contracts.system import HandshakeParams
from backend.contracts.task import (
    ResolveGrantParams,
    StartTaskExecutionParams,
    TaskIdParams,
    TaskStatus,
)
from backend.infrastructure.diagnostic_logging import configure_diagnostic_logging
from backend.infrastructure.plugin_package_lifecycle import LocalPluginPackageLifecycle
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import ErrorDomain, register_application_errors
from backend.rpc.framing import MAX_FRAME_BYTES, FrameTooLargeError
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
    # Python only keeps the closed package lifecycle surface. The install
    # handlers take the private host-gateway execution payloads: the full
    # plan arrives from a consumed WPF lease while the frozen public DTOs
    # stay in the renderer contract catalog.
    dispatcher.register(
        "plugin.inspectInstall", service.inspect_install, InspectInstallExecutionParams
    )
    dispatcher.register(
        "plugin.commitInstall", service.commit_install, CommitInstallExecutionParams
    )
    # plugin.cancelInstall has no worker registration: the WPF host owns
    # cancellation by taking and disposing its install-plan lease; the public
    # renderer capability keeps its frozen DTO in the Host contract catalog.
    dispatcher.register("plugin.upgrade", service.upgrade, UpgradePluginExecutionParams)
    dispatcher.register("plugin.rollback", service.rollback, RollbackPluginParams)
    dispatcher.register("plugin.uninstall", service.uninstall, UninstallPluginParams)
    dispatcher.register(
        "plugin.setEnabledExecution", service.set_enabled_execution, SetPluginEnabledExecutionParams
    )
    _register_retired_plugin_methods(dispatcher)


def _reject_retired_plugin_execution(_params: RetiredPluginExecutionParams) -> NoReturn:
    raise PluginRegistryError(
        "旧插件 Worker 执行入口已退役；请迁移到 API 3.x 隔离插件运行环境。",
        code="plugin_worker_retired",
    )


def _register_retired_plugin_methods(dispatcher: RpcDispatcher) -> None:
    register_application_errors(ErrorDomain.PLUGIN)
    for method in (
        "plugin.describeAction",
        "plugin.startAction",
        "plugin.resolveInteraction",
        "plugin.resolveFile",
        "plugin.cancelTask",
    ):
        dispatcher.register(method, _reject_retired_plugin_execution, RetiredPluginExecutionParams)


_TERMINAL_TASK_STATES = frozenset(("succeeded", "failed", "cancelled", "aborted"))


async def _send_execution_report(server: RpcServer, status: TaskStatus) -> None:
    """Send one ``task.executionReport``; degrade only oversized terminals.

    ``FrameTooLargeError`` is raised by ``write_frame`` before any byte is
    written, so an oversized terminal provably never partially landed and is
    replaced by one compact failure terminal (result dropped, no success/
    cancellation/rollback claim). Non-terminal reports and any other I/O
    failure propagate unchanged.
    """
    try:
        await server.notify("task.executionReport", status.model_dump(mode="json", by_alias=True))
    except FrameTooLargeError:
        if status.state not in _TERMINAL_TASK_STATES:
            raise
        logger.warning(
            "task %s terminal execution report exceeded the %d byte frame cap; "
            "delivering a compact failure terminal instead",
            status.task_id,
            MAX_FRAME_BYTES,
        )
        degraded = status.model_copy(
            update={
                "state": "failed",
                "result": None,
                "error": (
                    f"任务结果超出 RPC 帧上限（{MAX_FRAME_BYTES} 字节），终态报告未能送达宿主；"
                    "本次执行未向宿主交付结果。"
                ),
            }
        )
        await server.notify("task.executionReport", degraded.model_dump(mode="json", by_alias=True))


async def _build_server() -> tuple[
    RpcServer,
    PluginPlatformService | None,
]:
    server_ref: RpcServer | None = None

    async def notify_task_status(status: Any) -> None:
        if server_ref is not None:
            await _send_execution_report(server_ref, status)

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
        plugin_service = PluginPlatformService(
            registry=registry,
            store=store,
            package_lifecycle=package_lifecycle,
        )
        _register_plugin_methods(dispatcher, plugin_service)

    server = RpcServer(reader, StdoutAsyncWriter(sys.stdout.buffer), dispatcher)
    server_ref = server
    if plugin_service is None:
        _register_retired_plugin_methods(dispatcher)
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


def main() -> None:
    if sys.argv[1:] == ["--verify-retained-plugin-package"]:
        from backend.infrastructure.plugin_package_verifier import run

        raise SystemExit(run(sys.stdin.buffer, sys.stdout.buffer, sys.stderr.buffer))
    if sys.argv[1:]:
        raise SystemExit("unsupported backend mode")
    asyncio.run(_main())


if __name__ == "__main__":
    main()
