"""Legacy Insights error compatibility for the RPC error registry.

Layout execution was retired. Historical oracle capture runs the pinned producer;
current Python composition exposes no layout read, query, or write service.
"""

from backend.application.revisioned_metadata_port import JsonObject


class InsightsError(Exception):
    def __init__(self, message: str, *, code: str, field: str | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.field = field

    @property
    def rpc_error_data(self) -> JsonObject:
        result: JsonObject = {"code": self.code}
        if self.field is not None:
            result["field"] = self.field
        return result
