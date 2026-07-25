"""C1 task + path-grant application service.

Wraps the :class:`TaskRuntime` and :class:`SessionPathGrantStore` into the
JSON-RPC surface the host consumes:

* ``path.registerImportSource`` / ``path.registerExportTarget`` — the WPF picker
  calls these with the canonical path it chose (host-side only); the service
  issues a grant and returns the opaque descriptor the Web layer quotes.
* ``path.resolveGrant`` — returns the public descriptor for polling (never the
  raw path).
* ``task.create`` / ``task.cancel`` / ``task.status`` — the task lifecycle.

The file picker itself runs in WPF (``OpenFileDialog``/``SaveFileDialog``); the
Web layer never submits a raw path and only ever holds grant ids.
"""

from __future__ import annotations

from typing import Any

from backend.application.path_grant import PathGrantError, SessionPathGrantStore
from backend.application.task_runtime import TaskRuntime
from backend.contracts.task import (
    CreateTaskParams,
    HostExportTargetParams,
    HostImportSourceParams,
    RequestExportTargetGrantParams,
    RequestImportSourceGrantParams,
    ResolveGrantParams,
    SessionPathGrant,
    TaskIdParams,
    TaskStatus,
)


class TaskService:
    """JSON-RPC service for task lifecycle + path grants."""

    def __init__(
        self,
        runtime: TaskRuntime,
        grants: SessionPathGrantStore,
    ) -> None:
        self._runtime = runtime
        self._grants = grants

    # ------------------------------------------------------------------
    # Path grants
    # ------------------------------------------------------------------

    async def register_import_source(
        self, params: RequestImportSourceGrantParams
    ) -> SessionPathGrant:
        # The host calls this with the path it picked. In production the path
        # arrives from a privileged host-only RPC; this service never trusts a
        # renderer-supplied path. ``params`` carries the accept-filter hint
        # only (used by the picker, ignored here once the host has chosen).
        raise PathGrantError(
            "import-source registration is host-only; the renderer must request "
            "the picker via the WPF bridge",
            code="grant_host_only",
        )

    async def register_export_target(
        self, params: RequestExportTargetGrantParams
    ) -> SessionPathGrant:
        raise PathGrantError(
            "export-target registration is host-only; the renderer must request "
            "the picker via the WPF bridge",
            code="grant_host_only",
        )

    async def register_host_import_source(self, params: HostImportSourceParams) -> SessionPathGrant:
        return self._grants.issue(
            purpose="import_source",
            direction="read",
            path=params.path,
            size_bytes=params.size_bytes,
            mime_type=params.mime_type,
        )

    async def register_host_export_target(self, params: HostExportTargetParams) -> SessionPathGrant:
        return self._grants.issue(
            purpose="export_target",
            direction="write",
            path=params.path,
        )

    def issue_import_source(self, path: str, *, size_bytes: int | None = None) -> SessionPathGrant:
        """Host-only: issue a read grant for an import source path."""
        return self._grants.issue(
            purpose="import_source",
            direction="read",
            path=path,
            size_bytes=size_bytes,
        )

    def issue_export_target(self, path: str) -> SessionPathGrant:
        """Host-only: issue a write grant for an export target path."""
        return self._grants.issue(
            purpose="export_target",
            direction="write",
            path=path,
        )

    async def resolve_grant(self, params: ResolveGrantParams) -> SessionPathGrant:
        return self._grants.descriptor(params.grant_id)

    def resolve_path(self, grant_id: str, *, purpose: str, direction: str) -> str:
        """Broker-internal: resolve a grant to its canonical path."""
        return self._grants.resolve(grant_id, purpose=purpose, direction=direction)

    def consume_grant(self, grant_id: str) -> None:
        """Broker-internal: mark a grant consumed (single-use import sources)."""
        self._grants.consume(grant_id)

    # ------------------------------------------------------------------
    # Task lifecycle
    # ------------------------------------------------------------------

    async def create_task(self, params: CreateTaskParams) -> TaskStatus:
        return await self._runtime.create(params.kind, params.params)

    async def cancel_task(self, params: TaskIdParams) -> TaskStatus:
        return await self._runtime.cancel(params.task_id)

    async def status_task(self, params: TaskIdParams) -> TaskStatus:
        return self._runtime.status(params.task_id)

    @property
    def runtime(self) -> TaskRuntime:
        return self._runtime

    @property
    def grants(self) -> SessionPathGrantStore:
        return self._grants


def build_task_service(notification_sink: Any = None) -> TaskService:
    """Construct a :class:`TaskService` with a fresh runtime + grant store."""
    runtime = TaskRuntime(notification_sink=notification_sink)
    grants = SessionPathGrantStore()
    return TaskService(runtime, grants)


__all__ = ["TaskService", "build_task_service"]
