"""Typed application-error policy for the JSON-RPC seam.

The dispatcher owns envelopes and transport codes such as ``-32600``.  This
module owns the domain-facing policy: which exact exception type maps to which
stable product code, how optional structured data is merged, and when a
domain's errors are imported during composition.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol, runtime_checkable

from backend.application.system_service import ProtocolMismatchError
from backend.contracts.product_rpc import JsonObject, JsonValue

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
CODE_SURFACE = -32170
CODE_CONTENT_MODEL = -32180


class ErrorDomain(StrEnum):
    """Application domains whose concrete errors are enabled at composition."""

    PASTE = "paste"
    PATH_GRANT = "path_grant"
    IMPORT = "import"
    EXPORT = "export"
    INSIGHTS = "insights"
    SURFACE = "surface"
    CONTENT_MODEL = "content_model"
    SETTINGS_COMMAND = "settings_command"
    PLUGIN = "plugin"


@dataclass(frozen=True)
class RpcError:
    code: int
    message: str
    data: JsonObject


@runtime_checkable
class RpcErrorDataProvider(Protocol):
    """Application error whose additional wire data is closed JSON."""

    @property
    def rpc_error_data(self) -> JsonObject: ...


@dataclass(frozen=True)
class _ErrorSpec:
    code: int
    message: str
    kind: str


@dataclass(frozen=True)
class _InvalidJsonValue:
    pass


_INVALID_JSON_VALUE = _InvalidJsonValue()


class RpcErrorRegistry:
    """Resolve exact application exception types to stable JSON-RPC errors."""

    def __init__(self) -> None:
        self._specs: dict[type[Exception], _ErrorSpec] = {}

    def register(
        self,
        error_type: type[Exception],
        *,
        code: int,
        message: str,
        kind: str,
    ) -> None:
        self._specs[error_type] = _ErrorSpec(code=code, message=message, kind=kind)

    def register_once(
        self,
        error_type: type[Exception],
        *,
        code: int,
        message: str,
        kind: str,
    ) -> None:
        self._specs.setdefault(
            error_type,
            _ErrorSpec(code=code, message=message, kind=kind),
        )

    def enable(self, *domains: ErrorDomain) -> None:
        """Lazily load and register each requested application domain."""

        for domain in domains:
            for error_type, spec in _domain_specs(domain):
                self.register_once(
                    error_type,
                    code=spec.code,
                    message=spec.message,
                    kind=spec.kind,
                )

    def resolve(self, error: Exception) -> RpcError | None:
        """Return the public error for an exact registered exception type."""

        spec = self._specs.get(type(error))
        if spec is None:
            return None
        data: JsonObject = {"kind": spec.kind, "message": str(error)}
        if isinstance(error, RpcErrorDataProvider):
            extra = _closed_json_object(error.rpc_error_data)
            if extra is not None:
                data.update(extra)
        return RpcError(code=spec.code, message=spec.message, data=data)


def _closed_json_object(value: object) -> JsonObject | None:
    if not isinstance(value, dict) or not all(isinstance(key, str) for key in value):
        return None
    result: JsonObject = {}
    for key, item in value.items():
        closed = _closed_json_value(item)
        if isinstance(closed, _InvalidJsonValue):
            return None
        result[key] = closed
    return result


def _closed_json_value(value: object) -> JsonValue | _InvalidJsonValue:
    if value is None or isinstance(value, (str, bool, int)):
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            return _INVALID_JSON_VALUE
        return value
    if isinstance(value, list):
        result: list[JsonValue] = []
        for item in value:
            closed = _closed_json_value(item)
            if isinstance(closed, _InvalidJsonValue):
                return _INVALID_JSON_VALUE
            result.append(closed)
        return result
    if isinstance(value, dict):
        closed_object = _closed_json_object(value)
        return _INVALID_JSON_VALUE if closed_object is None else closed_object
    return _INVALID_JSON_VALUE


def _domain_specs(domain: ErrorDomain) -> tuple[tuple[type[Exception], _ErrorSpec], ...]:
    if domain is ErrorDomain.PASTE:
        from backend.application.paste_service import PasteError

        return ((PasteError, _ErrorSpec(CODE_PASTE, "Paste error", "paste_error")),)
    if domain is ErrorDomain.PATH_GRANT:
        from backend.application.path_grant import PathGrantError

        return (
            (PathGrantError, _ErrorSpec(CODE_PATH_GRANT, "Path grant error", "path_grant_error")),
        )
    if domain is ErrorDomain.IMPORT:
        from backend.application.import_service import ImportFlowError

        return ((ImportFlowError, _ErrorSpec(CODE_IMPORT, "Import error", "import_error")),)
    if domain is ErrorDomain.EXPORT:
        from backend.application.export_service import ExportError

        return ((ExportError, _ErrorSpec(CODE_EXPORT, "Export error", "export_error")),)
    if domain is ErrorDomain.INSIGHTS:
        from backend.application.insights_service import InsightsError

        return ((InsightsError, _ErrorSpec(CODE_INSIGHTS, "Insights error", "insights_error")),)
    if domain is ErrorDomain.SURFACE:
        from backend.application.surface_service import SurfaceError

        return ((SurfaceError, _ErrorSpec(CODE_SURFACE, "Interface error", "surface_error")),)
    if domain is ErrorDomain.CONTENT_MODEL:
        from backend.application.content_model_service import ContentModelError

        return (
            (
                ContentModelError,
                _ErrorSpec(CODE_CONTENT_MODEL, "Content model error", "content_model_error"),
            ),
        )
    if domain is ErrorDomain.SETTINGS_COMMAND:
        from backend.application.settings_command_service import SettingsCommandError

        return (
            (
                SettingsCommandError,
                _ErrorSpec(
                    CODE_SETTINGS_COMMAND,
                    "Settings/command error",
                    "settings_command_error",
                ),
            ),
        )
    if domain is ErrorDomain.PLUGIN:
        from backend.application.plugin_registry import PluginRegistryError
        from backend.infrastructure.plugin_package import PluginPackageError
        from backend.infrastructure.plugin_schema import PluginSchemaError

        spec = _ErrorSpec(CODE_PLUGIN, "Plugin error", "plugin_error")
        return tuple(
            (error_type, spec)
            for error_type in (
                PluginRegistryError,
                PluginPackageError,
                PluginSchemaError,
            )
        )
    raise ValueError(f"unsupported application error domain: {domain}")


application_error_registry = RpcErrorRegistry()
application_error_registry.register(
    ProtocolMismatchError,
    code=CODE_PROTOCOL_MISMATCH,
    message="Protocol mismatch",
    kind="protocol_mismatch",
)


def register_application_errors(*domains: ErrorDomain) -> None:
    """Enable built-in domain errors on the process-wide registry."""

    application_error_registry.enable(*domains)


def register_rpc_error(
    error_type: type[Exception],
    *,
    code: int,
    message: str,
    kind: str,
) -> None:
    """Register one adapter-owned error on the process-wide registry."""

    application_error_registry.register(
        error_type,
        code=code,
        message=message,
        kind=kind,
    )


__all__ = [
    "CODE_CONTENT_MODEL",
    "CODE_PRODUCT_DATA",
    "CODE_SURFACE",
    "ErrorDomain",
    "RpcError",
    "RpcErrorRegistry",
    "application_error_registry",
    "register_application_errors",
    "register_rpc_error",
]
