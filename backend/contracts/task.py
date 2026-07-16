"""C1 TaskRuntime + path-grant contracts.

These contracts describe the cancellable, observable task runtime and the
short-lived session path grants that back file import/export. They are the
foundation every C1 data-IO operation (import preview/apply, export, relation
expansion) is built on.

Design notes
------------
* Tasks own their lifecycle: ``queued → running → succeeded/failed/cancelled``.
  Local-side-effect tasks that the runtime cannot resume after a process
  restart are marked ``aborted`` (never silently replayed). Remote mutations
  rely on their idempotency key for result verification, not on task replay.
* Progress is reported as a monotonic ``(done, total)`` pair plus a human
  ``message``; the host emits ``task.progress`` notifications so the UI can show
  real progress without polling.
* A :class:`SessionPathGrant` binds a path the WPF file picker chose to the
  session, a purpose (import source / export target), a read/write direction
  and a short expiry. The Web layer only ever holds the opaque grant id +
  display metadata — never the raw path.

Wire conventions
----------------
* Field aliases use ``camelCase``; Python attributes stay ``snake_case``.
* ``populate_by_name=True`` accepts both forms.
* ``extra="forbid`` rejects unknown keys.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    """Shared base for task-domain contracts."""

    model_config = _camel_config()


#: The closed set of task lifecycle states.
TaskState = Literal["queued", "running", "succeeded", "failed", "cancelled", "aborted"]

#: Why a task ended. ``aborted`` is reserved for local-side-effect tasks that
#: could not be resumed after a process restart.
TaskOutcome = Literal["succeeded", "failed", "cancelled", "aborted"]


class TaskProgress(CamelModel):
    """Monotonic progress for a running task.

    Wire form:: ``{"done": 42, "total": 100, "message": "Importing row 42…"}``

    ``done``/``total`` are best-effort counts; when ``total`` is unknown it is
    ``0`` and the UI shows an indeterminate indicator. ``done`` never decreases.
    """

    done: int = Field(default=0, ge=0)
    total: int = Field(default=0, ge=0)
    message: str = Field(default="", max_length=512)


class TaskStatus(CamelModel):
    """The current status of a task.

    Wire form::

        {"taskId": "task-1", "kind": "data.import", "state": "running",
         "progress": {"done": 42, "total": 100, "message": "..."},
         "result": null, "error": null}

    ``result`` carries the typed payload on ``succeeded`` (the specific task
    kind defines its shape). ``error`` carries a UI-ready message on ``failed``.
    """

    task_id: str = Field(min_length=1, max_length=128)
    kind: str = Field(min_length=1, max_length=64)
    state: TaskState
    progress: TaskProgress = Field(default_factory=TaskProgress)
    result: Any = None
    error: str | None = Field(default=None, max_length=1024)


class CreateTaskParams(CamelModel):
    """Parameters for ``task.create``.

    Wire form::

        {"kind": "data.import", "params": {...}}

    ``kind`` identifies the task handler registered in the runtime (e.g.
    ``data.import``, ``data.export``). ``params`` is the handler-specific
    payload, validated by the handler (not by this envelope).
    """

    kind: str = Field(min_length=1, max_length=64)
    params: dict[str, Any] = Field(default_factory=dict)


class TaskIdParams(CamelModel):
    """Parameters for ``task.cancel`` / ``task.status``.

    Wire form:: ``{"taskId": "task-1"}``
    """

    task_id: str = Field(min_length=1, max_length=128)


# ---------------------------------------------------------------------------
# Session path grants
# ---------------------------------------------------------------------------

#: The purpose a path grant authorizes. ``import_source`` is a read grant on a
#: file the user picked for import; ``export_target`` is a write grant on a file
#: the user picked (or will pick) for export output.
PathGrantPurpose = Literal["import_source", "export_target"]

#: The direction of access the grant permits.
PathGrantDirection = Literal["read", "write"]


class SessionPathGrant(CamelModel):
    """A short-lived, session-bound authorization to access one file path.

    Wire form::

        {"grantId": "grant-1", "purpose": "import_source", "direction": "read",
         "displayName": "contracts.xlsx", "sizeBytes": 18432,
         "expiresAt": 1.6e9}

    The Web layer receives ONLY this opaque descriptor: the grant id (to quote
    in subsequent RPC calls), a display name + size for the UI, and an expiry.
    The canonical path stays inside the Python broker (and the WPF picker) and
    never crosses the WebView boundary. ``expires_at`` is a Unix timestamp.
    """

    grant_id: str = Field(min_length=1, max_length=128)
    purpose: PathGrantPurpose
    direction: PathGrantDirection
    display_name: str = Field(default="", max_length=256)
    size_bytes: int | None = Field(default=None, ge=0)
    mime_type: str | None = Field(default=None, max_length=128)
    expires_at: float


class RequestImportSourceGrantParams(CamelModel):
    """Parameters for ``path.requestImportSource``.

    Wire form:: ``{"accept": [".xlsx", ".csv"]}``

    The host opens the WPF ``OpenFileDialog`` (never trusting a renderer path);
    ``accept`` is the filter the dialog applies.
    """

    accept: list[str] = Field(default_factory=lambda: [".xlsx", ".xls", ".csv"])


class RequestExportTargetGrantParams(CamelModel):
    """Parameters for ``path.requestExportTarget``.

    Wire form:: ``{"defaultName": "contracts-export.csv", "format": "csv"}``

    The host opens the WPF ``SaveFileDialog`` with the suggested default name.
    On confirm the host atomically finalizes the output to the chosen path.
    """

    default_name: str = Field(default="", max_length=256)
    format: str = Field(default="csv", max_length=16)


class ResolveGrantParams(CamelModel):
    """Parameters for ``path.resolve`` (internal: broker-side grant lookup).

    Wire form:: ``{"grantId": "grant-1"}``

    Resolving a grant returns the canonical path to the broker-internal handler
    only; the RPC result is the public :class:`SessionPathGrant` descriptor
    (never the raw path).
    """

    grant_id: str = Field(min_length=1, max_length=128)


__all__ = [
    "CamelModel",
    "CreateTaskParams",
    "PathGrantDirection",
    "PathGrantPurpose",
    "RequestExportTargetGrantParams",
    "RequestImportSourceGrantParams",
    "ResolveGrantParams",
    "SessionPathGrant",
    "TaskIdParams",
    "TaskOutcome",
    "TaskProgress",
    "TaskState",
    "TaskStatus",
]
