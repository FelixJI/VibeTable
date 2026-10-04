"""Read-only import result projection; execution remains owned by the Host."""

from typing import Literal

from pydantic import Field, model_validator

from backend.contracts.task import CamelModel


class ImportHistoryParams(CamelModel):
    """The active workspace comes from the Host lease, never renderer input."""


class ImportHistoryEntry(CamelModel):
    task_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    source_type: Literal["csv", "xlsx"]
    source_name: str = Field(min_length=1, max_length=256)
    state: Literal[
        "queued", "running", "interrupted", "succeeded", "failed", "cancelled", "aborted"
    ]
    commit_state: Literal["unknown", "committed"]
    created_count: int | None = Field(ge=0)
    updated_count: int | None = Field(ge=0)
    started_at: str
    finished_at: str | None
    session_epoch: int = Field(ge=1)
    error_code: str | None

    @model_validator(mode="after")
    def validate_receipt(self) -> ImportHistoryEntry:
        if self.commit_state == "unknown":
            if self.created_count is not None or self.updated_count is not None:
                raise ValueError("Unknown commits must not claim a row count")
            if self.state == "succeeded":
                raise ValueError("Success requires a committed receipt")
        elif (
            self.state != "succeeded"
            or self.created_count is None
            or self.updated_count is None
            or self.finished_at is None
        ):
            raise ValueError("Committed imports require their terminal counts and time")
        return self


class ImportHistoryResult(CamelModel):
    items: list[ImportHistoryEntry] = Field(max_length=200)
