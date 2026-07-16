from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


class RpcRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    jsonrpc: Literal["2.0"]
    id: str | int | None = None
    method: str = Field(min_length=1, max_length=128)
    params: dict[str, Any] = Field(default_factory=dict)


class RpcErrorObject(BaseModel):
    code: int
    message: str
    data: dict[str, Any] | None = None


class RpcResponse(BaseModel):
    jsonrpc: Literal["2.0"] = "2.0"
    id: str | int | None
    result: Any | None = None
    error: RpcErrorObject | None = None
