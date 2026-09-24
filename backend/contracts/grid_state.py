"""Grid-state contracts for the Host-owned presentation state.

Production presentation state is owned by the WPF Host and scoped by workspace
UUID and table; ``HostGridState`` models describe that boundary. The legacy
Python service models were removed with the retired Python route; the frozen
producer behavior is pinned by ``contracts/v2/grid_state-python-oracle.json``.

Wire conventions
----------------

* Field aliases use ``camelCase``; Python attributes stay ``snake_case``.
* ``populate_by_name=True`` accepts both forms.
* ``extra="forbid"`` rejects unknown keys so a stale client cannot silently
  drop a new state field.

Design notes
------------

* State excludes row data and pending edits — only view/layout preferences.
* ``revision`` is an opaque string the host carries; a stale revision on save
  is a conflict (the host must re-read, merge, and retry).
* Columns are keyed by name; columns missing from the saved state on load are
  pruned, and newly-added columns keep their default visibility.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic.alias_generators import to_camel

from backend.contracts.query import FilterExpression, SortCondition, TableQuery


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    """Shared base for grid-state contracts."""

    model_config = _camel_config()


class ColumnState(CamelModel):
    """One column's persisted grid state.

    Wire form::

        {"name": "amount", "width": 120, "visible": true,
         "frozen": false, "order": 2}

    * ``name`` is the schema column name (verified against the live schema on
      load; unknown columns are pruned).
    * ``width`` is in CSS pixels (null = default).
    * ``visible`` controls column visibility (default true).
    * ``frozen`` marks a frozen/pinned column (default false).
    * ``order`` is the 0-based position in the column order (null = append).
    """

    name: str = Field(min_length=1, max_length=128)
    width: int | None = Field(default=None, ge=1, le=4096)
    visible: bool = True
    frozen: bool = False
    order: int | None = Field(default=None, ge=0)


class HostGridState(CamelModel):
    """Device presentation overlay; the shared preset remains the baseline authority."""

    columns: list[ColumnState] = Field(default_factory=list, max_length=512)
    sorts: list[SortCondition] = Field(default_factory=list, max_length=16)
    filters: list[FilterExpression] = Field(default_factory=list, max_length=50)
    keyword: str | None = Field(default=None, max_length=256)
    density: Literal["compact", "comfortable", "cozy"] = "comfortable"
    forced_remote: bool = False
    revision: str | None = None
    preset_id: str | None = Field(default=None, min_length=1, max_length=128)
    preset_revision: str | None = Field(default=None, min_length=1, max_length=256)

    @model_validator(mode="after")
    def validate_presentation(self) -> HostGridState:
        if (self.preset_id is None) != (self.preset_revision is None):
            raise ValueError("preset identity and revision must be supplied together")
        TableQuery(filters=self.filters, sorts=self.sorts, keyword=self.keyword)
        return self


class HostGridStateResult(CamelModel):
    """Host-confirmed state and CAS outcome, never a Python-owned record."""

    state: HostGridState
    revision: str
    conflict: bool = False


class HostGridStateGetParams(CamelModel):
    """Host-owned request; workspace identity comes from the trusted wire scope."""

    table: str = Field(min_length=1, max_length=128)


class HostGridStateSaveParams(HostGridStateGetParams):
    """Complete presentation update with an explicitly supplied CAS revision."""

    state: HostGridState
    revision: str | None = Field(...)
