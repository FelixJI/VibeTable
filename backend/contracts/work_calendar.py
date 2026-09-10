"""Wire models for the Go-owned shared work calendar."""

from typing import Literal

from pydantic import Field

from backend.contracts.settings_commands import CamelModel


class WorkCalendarOverride(CamelModel):
    date: str = Field(pattern=r"^\d{4}-\d{2}-\d{2}$")
    kind: Literal["holiday", "workday"]
    name: str = Field(max_length=40)


class ReadWorkCalendarParams(CamelModel):
    pass


class CommitWorkCalendarParams(CamelModel):
    overrides: list[WorkCalendarOverride] = Field(max_length=3660)
    expected_revision: str
    idempotency_key: str = Field(min_length=1, max_length=192)


class WorkCalendarResult(CamelModel):
    overrides: list[WorkCalendarOverride]
    revision: str


class WorkCalendarReceipt(WorkCalendarResult):
    status: Literal["applied", "replayed"]
    change_set_id: str
    emitted_events: list[str]
