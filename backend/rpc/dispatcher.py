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

Typed application errors are resolved by :mod:`backend.rpc.error_registry`.
The registry deliberately matches exact exception types, so an unregistered
subclass remains an internal error instead of inheriting an accidental wire
contract.

The B1 mutation errors (:class:`EditConflictError`,
:class:`MutationValidationError`) carry **extra structured data** beyond the
generic ``{kind, message}`` envelope: a conflict carries ``currentRow`` and a
validation error carries ``fieldErrors``. Application errors may expose an
``rpc_error_data`` attribute; the registry merges it while preserving the
generic envelope.

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

from backend.rpc.error_registry import application_error_registry
from backend.rpc.messages import RpcRequest

logger = logging.getLogger(__name__)

#: JSON-RPC error codes used by this dispatcher.
CODE_INVALID_REQUEST = -32600
CODE_METHOD_NOT_FOUND = -32601
CODE_INVALID_PARAMS = -32602
CODE_INTERNAL_ERROR = -32603
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
            mapped = application_error_registry.resolve(exc)
            if mapped is not None:
                return self._error_response(
                    request.id,
                    mapped.code,
                    mapped.message,
                    data=mapped.data,
                )
            logger.exception(
                "rpc.handler_failed",
                extra={
                    "errorCode": type(exc).__name__,
                    "operationId": request.method,
                },
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
                "rpc.result_serialization_failed",
                extra={
                    "errorCode": type(exc).__name__,
                    "operationId": request.method,
                },
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
