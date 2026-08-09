"""JSON-RPC 2.0 dispatcher.

Validates inbound request frames, looks up the registered handler, validates
params with the registered Pydantic model, awaits the handler, and serializes
the result by alias. Errors are mapped to JSON-RPC codes:

================  ===========================  =========================
JSON-RPC code     Name                         Source
================  ===========================  =========================
``-32600``        Invalid Request              ``RpcRequest`` validation
``-32601``        Method not found             unknown ``method``
``-32602``        Invalid params               params model validation
``-32001``        Protocol mismatch            ``ProtocolMismatchError``
``-32010``        Edit conflict                ``EditConflictError`` (B1)
``-32011``        Mutation validation          ``MutationValidationError`` (B1)
``-32020``        Table not found              ``TableNotFoundError``
``-32021``        Invalid argument             ``InvalidArgumentError`` /
                                              ``QueryCompileError`` (B3)
``-32022``        Database not found           ``DatabaseNotFoundError``
``-32603``        Internal error               any other handler exception
================  ===========================  =========================

Typed application errors are mapped to ``(code, kind)`` pairs through the
``_APP_ERROR_MAP`` registry below. To add a new mapping, extend both the table
and the ``CODE_*`` constant. Application errors must NOT subclass each other:
the dispatcher iterates the registry in definition order, so common base
classes would shadow their more specific subclasses.

The B1 mutation errors (:class:`EditConflictError`,
:class:`MutationValidationError`) carry **extra structured data** beyond the
generic ``{kind, message}`` envelope: a conflict carries ``currentRow`` and a
validation error carries ``fieldErrors``. Application errors may expose an
``rpc_error_data`` attribute returning a dict that the dispatcher merges into
the error object's ``data``; the registry's ``kind``/``message`` are still
applied so the generic envelope stays stable.

B3 reuses the existing ``-32021`` invalid-argument code for query-compile
errors (unknown columns, bad operator arity, remote-regex rejection): they
share the same ``invalid_argument`` kind so no new protocol code is needed.
``QueryCompileError`` carries an optional ``field`` in its ``rpc_error_data``
to tell the host which column triggered the rejection.

Notifications (requests with no ``id``) are dispatched for side effects but
produce no response object — ``dispatch`` returns ``None`` for them.
"""

from __future__ import annotations

import inspect
import logging
from collections.abc import Callable
from typing import Any

from pydantic import BaseModel, ValidationError
from pydantic_core import to_jsonable_python

from backend.application.system_service import ProtocolMismatchError
from backend.rpc.messages import RpcRequest

logger = logging.getLogger(__name__)

#: JSON-RPC error codes used by this dispatcher.
CODE_INVALID_REQUEST = -32600
CODE_METHOD_NOT_FOUND = -32601
CODE_INVALID_PARAMS = -32602
CODE_INTERNAL_ERROR = -32603
CODE_PROTOCOL_MISMATCH = -32001
CODE_EDIT_CONFLICT = -32010
CODE_MUTATION_VALIDATION = -32011
CODE_TABLE_NOT_FOUND = -32020
CODE_INVALID_ARGUMENT = -32021
CODE_DATABASE_NOT_FOUND = -32022
CODE_PASTE = -32040
CODE_PATH_GRANT = -32050
CODE_IMPORT = -32060
CODE_EXPORT = -32061
CODE_INSIGHTS = -32080
CODE_SETTINGS_COMMAND = -32100
CODE_PLUGIN = -32120
CODE_PRODUCT_DATA = -32150
CODE_IDENTIFIER = -32160

#: Maps each typed application error class to a ``(code, message, kind)``
#: tuple. Order matters only if classes share a base class — they do not here.
#: Keep ``Exception`` *out* of this map: the dispatcher falls back to
#: ``CODE_INTERNAL_ERROR`` for any exception not listed.
_APP_ERROR_MAP: dict[type[Exception], tuple[int, str, str]] = {
    ProtocolMismatchError: (CODE_PROTOCOL_MISMATCH, "Protocol mismatch", "protocol_mismatch"),
}


def register_rpc_error(
    error_type: type[Exception],
    *,
    code: int,
    message: str,
    kind: str,
) -> None:
    """Register one typed error without importing its concrete layer here."""

    _APP_ERROR_MAP[error_type] = (code, message, kind)


def register_paste_errors() -> None:
    """Register B2 paste errors without importing the service at startup."""
    from backend.application.paste_service import PasteError

    if PasteError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[PasteError] = (
            CODE_PASTE,
            "Paste error",
            "paste_error",
        )


def register_path_grant_errors() -> None:
    """Register C1 path-grant errors without importing the service at startup."""
    from backend.application.path_grant import PathGrantError

    if PathGrantError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[PathGrantError] = (
            CODE_PATH_GRANT,
            "Path grant error",
            "path_grant_error",
        )


def register_import_errors() -> None:
    """Register C1 import errors without importing the service at startup."""
    from backend.application.import_service import ImportFlowError

    if ImportFlowError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[ImportFlowError] = (
            CODE_IMPORT,
            "Import error",
            "import_error",
        )


def register_export_errors() -> None:
    """Register C1 export errors without importing the service at startup."""
    from backend.application.export_service import ExportError

    if ExportError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[ExportError] = (
            CODE_EXPORT,
            "Export error",
            "export_error",
        )


def register_identifier_errors() -> None:
    """Register product identifier-management errors."""
    from backend.application.identifier_mapping_service import (
        IdentifierManagementError,
    )

    if IdentifierManagementError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[IdentifierManagementError] = (
            CODE_IDENTIFIER,
            "Identifier mapping error",
            "identifier_mapping_error",
        )


def register_insights_errors() -> None:
    """Register C2 insights errors without importing the service at startup."""
    from backend.application.insights_service import InsightsError

    if InsightsError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[InsightsError] = (
            CODE_INSIGHTS,
            "Insights error",
            "insights_error",
        )


def register_settings_command_errors() -> None:
    """Register D2 settings/command errors without importing the service at startup."""
    from backend.application.settings_command_service import SettingsCommandError

    if SettingsCommandError not in _APP_ERROR_MAP:
        _APP_ERROR_MAP[SettingsCommandError] = (
            CODE_SETTINGS_COMMAND,
            "Settings/command error",
            "settings_command_error",
        )


def register_plugin_errors() -> None:
    """Register stable plugin-domain failures without loading them at startup."""

    from backend.application.plugin_registry import PluginRegistryError
    from backend.infrastructure.plugin_package import PluginPackageError
    from backend.infrastructure.plugin_schema import PluginSchemaError

    for error_type in (
        PluginRegistryError,
        PluginPackageError,
        PluginSchemaError,
    ):
        if error_type not in _APP_ERROR_MAP:
            _APP_ERROR_MAP[error_type] = (
                CODE_PLUGIN,
                "Plugin error",
                "plugin_error",
            )


Handler = Callable[..., Any]
ParamsModel = type[BaseModel]


class RpcDispatcher:
    """Registry of JSON-RPC method handlers."""

    def __init__(self) -> None:
        self._handlers: dict[str, tuple[Handler, ParamsModel, bool]] = {}

    @property
    def registered_methods(self) -> tuple[str, ...]:
        """Return the currently registered method names in deterministic order."""
        return tuple(sorted(self._handlers))

    def register(self, method: str, handler: Handler, params_model: ParamsModel) -> None:
        """Register ``handler`` for ``method``.

        ``params_model`` validates the request. Handlers whose parameter names
        match model fields receive validated keyword arguments; DTO-oriented
        handlers receive the model instance. Sync and async handlers are both
        supported so pure local services need no fake coroutine wrapper.
        """
        parameters = [
            parameter
            for parameter in inspect.signature(handler).parameters.values()
            if parameter.kind
            in (
                inspect.Parameter.POSITIONAL_ONLY,
                inspect.Parameter.POSITIONAL_OR_KEYWORD,
                inspect.Parameter.KEYWORD_ONLY,
            )
        ]
        receives_model = len(parameters) == 1 and parameters[0].name in {"params", "_params"}
        unpack_params = (
            bool(parameters)
            and not receives_model
            and all(parameter.name in params_model.model_fields for parameter in parameters)
        )
        self._handlers[method] = (handler, params_model, unpack_params)

    async def dispatch(self, payload: dict[str, Any]) -> dict[str, Any] | None:
        """Dispatch one decoded request frame.

        Returns the JSON-RPC response object (ready for framing) or ``None``
        for notifications (requests without an ``id``).
        """
        # --- 1. Validate the request envelope ----------------------------
        try:
            request = RpcRequest.model_validate(payload)
        except ValidationError:
            return self._error_response(
                payload.get("id"),
                CODE_INVALID_REQUEST,
                "Invalid Request",
            )

        is_notification = request.id is None

        # --- 2. Look up the handler --------------------------------------
        registered = self._handlers.get(request.method)
        if registered is None:
            if is_notification:
                return None
            return self._error_response(
                request.id,
                CODE_METHOD_NOT_FOUND,
                "Method not found",
            )

        handler, params_model, unpack_params = registered

        # --- 3. Validate params ------------------------------------------
        try:
            params = params_model.model_validate(request.params)
        except ValidationError:
            if is_notification:
                return None
            return self._error_response(
                request.id,
                CODE_INVALID_PARAMS,
                "Invalid params",
            )

        # --- 4. Await the handler ----------------------------------------
        try:
            if unpack_params:
                kwargs = {
                    name: getattr(params, name)
                    for name in params_model.model_fields
                    if name in inspect.signature(handler).parameters
                }
                result = handler(**kwargs)
            else:
                result = handler(params)
            if inspect.isawaitable(result):
                result = await result
        except Exception as exc:
            if is_notification:
                return None
            mapping = _APP_ERROR_MAP.get(type(exc))
            if mapping is not None:
                code, message, kind = mapping
                # B1 mutation errors carry extra structured data (currentRow,
                # fieldErrors) exposed via ``rpc_error_data``. Merge it into
                # the generic envelope.
                data: dict[str, Any] = {"kind": kind, "message": str(exc)}
                extra = getattr(exc, "rpc_error_data", None)
                if isinstance(extra, dict):
                    data.update(extra)
                return self._error_response(
                    request.id,
                    code,
                    message,
                    data=data,
                )
            logger.exception(
                "rpc handler failed: method=%s exception=%s",
                request.method,
                type(exc).__name__,
            )
            return self._error_response(
                request.id,
                CODE_INTERNAL_ERROR,
                "Internal error",
            )

        if is_notification:
            return None

        # --- 5. Serialize the result by alias ----------------------------
        # Results are not always a single top-level model. Collection RPCs
        # (for example plugin.listAudit) return lists containing Pydantic
        # models and datetime values. Passing those objects through to the
        # framing layer makes json.dumps fail after the handler succeeded,
        # which strands the correlated caller without a response.
        try:
            serialized = to_jsonable_python(result, by_alias=True)
        except Exception as exc:
            logger.exception(
                "rpc result serialization failed: method=%s result=%s exception=%s",
                request.method,
                type(result).__name__,
                type(exc).__name__,
            )
            return self._error_response(
                request.id,
                CODE_INTERNAL_ERROR,
                "Internal error",
            )
        return {
            "jsonrpc": "2.0",
            "id": request.id,
            "result": serialized,
        }

    @staticmethod
    def _error_response(
        request_id: str | int | None,
        code: int,
        message: str,
        *,
        data: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        error: dict[str, Any] = {"code": code, "message": message}
        if data is not None:
            error["data"] = data
        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "error": error,
        }
