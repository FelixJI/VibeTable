"""Worker task lifecycle; all native file grants belong to the owning Host."""

from __future__ import annotations

import asyncio

from backend.application.host_files import HostFiles
from backend.application.path_grant import PathGrantError
from backend.application.task_runtime import HostExecutionRuntime, NotificationSink
from backend.contracts.task import (
    ExportTargetSettled,
    ResolveGrantParams,
    StartTaskExecutionParams,
    TaskIdParams,
)


class TaskService:
    def __init__(self, runtime: HostExecutionRuntime, files: HostFiles) -> None:
        self._runtime = runtime
        self.files = files
        self._export_admission = asyncio.Lock()

    async def start_execution(self, params: StartTaskExecutionParams) -> dict[str, bool]:
        grant_id = params.params.get("grantId") if params.kind == "data.export" else None
        if params.kind == "data.export":
            if not isinstance(grant_id, str):
                raise ValueError("Export requires a grantId.")
            async with self._export_admission:
                descriptor = await self.files.describe(grant_id)
                if descriptor.purpose != "export_target" or descriptor.direction != "write":
                    raise PathGrantError(
                        "Export requires a writable Host grant.", code="grant_direction_mismatch"
                    )
                await self._runtime.start(
                    params.task_id, params.kind, params.params, export_grant_id=grant_id
                )
        else:
            await self._runtime.start(params.task_id, params.kind, params.params)
        return {"accepted": True}

    async def cancel_execution(self, params: TaskIdParams) -> bool:
        return await self._runtime.cancel(params.task_id)

    async def settle_export(self, params: ResolveGrantParams) -> ExportTargetSettled:
        # The Host revokes native access before calling this worker-only join.
        async with self._export_admission:
            await self._runtime.settle_export_grant(params.grant_id)
        return ExportTargetSettled(grant_id=params.grant_id, settled=True)

    @property
    def runtime(self) -> HostExecutionRuntime:
        return self._runtime


def build_task_service(
    *, files: HostFiles, notification_sink: NotificationSink | None = None
) -> TaskService:
    return TaskService(HostExecutionRuntime(notification_sink=notification_sink), files)
