"""Stable errors raised by the Directus adapter boundary."""


class DirectusAdapterError(ValueError):
    """Base class for deterministic adapter/configuration failures."""


class DirectusQueryError(DirectusAdapterError):
    """Raised when a VibeTable query cannot be represented safely in Directus."""

    def __init__(self, message: str, *, field: str | None = None) -> None:
        super().__init__(message)
        self.field = field


class DirectusSchemaError(DirectusAdapterError):
    """Raised when Directus field/permission metadata is inconsistent."""


class DirectusTransportError(DirectusAdapterError):
    """Sanitized HTTP/API failure returned by Directus."""

    def __init__(
        self,
        message: str,
        *,
        status: int | None = None,
        code: str | None = None,
        field_errors: dict[str, str] | None = None,
    ) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.field_errors = field_errors or {}

    @property
    def rpc_error_data(self) -> dict[str, object]:
        return {
            "status": self.status,
            "code": self.code,
            "fieldErrors": self.field_errors,
        }


class DirectusSessionError(DirectusAdapterError):
    """Raised when no usable local Directus session exists."""
