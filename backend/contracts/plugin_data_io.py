"""Closed Host-only composition metadata; never a plugin Worker capability."""

from pydantic import BaseModel, ConfigDict, Field

from backend.contracts.product_rpc import JsonObject


class PluginIoScope(BaseModel):
    model_config = ConfigDict(extra="forbid", populate_by_name=True)
    principal: JsonObject
    run_id: str = Field(alias="runId", min_length=1, max_length=128)
    fields: list[str] = Field(default_factory=list, max_length=256)
    query: JsonObject = Field(default_factory=dict)
