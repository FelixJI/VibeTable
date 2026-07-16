"""B3 grid-state contracts: the on-the-wire shape of the durable per-table grid
state (column widths, order, visibility, frozen columns, sort/filter/search,
density and forced-remote preference).

Grid state is stored through Python in the **local user-state database**
(``%LOCALAPPDATA%/VibeTable/state/vibetable-state.db``), never in the business SQLite
database and never solely in WebView localStorage. This keeps state durable
across restarts and shareable across hosts, while leaving the business schema
untouched.

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

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


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


class GridState(CamelModel):
    """The full persisted grid state for one table.

    Wire form::

        {"columns": [...], "sorts": [...], "filters": [...],
         "keyword": null, "density": "comfortable",
         "forcedRemote": false, "revision": "rev-3"}

    * ``columns`` is the per-column layout state.
    * ``sorts`` / ``filters`` / ``keyword`` mirror the B3 query AST so the
      grid restores the exact view the user left.
    * ``density`` is a UI hint (``compact`` / ``comfortable`` / ``cozy``).
    * ``forced_remote`` records the user's per-table remote-mode preference.
    * ``revision`` is the opaque conflict token carried on save.
    """

    columns: list[ColumnState] = Field(default_factory=list, max_length=512)
    sorts: list[dict[str, Any]] = Field(default_factory=list, max_length=16)
    filters: list[dict[str, Any]] = Field(default_factory=list, max_length=64)
    keyword: str | None = Field(default=None, max_length=256)
    density: Literal["compact", "comfortable", "cozy"] = "comfortable"
    forced_remote: bool = False
    revision: str | None = None


class GridStateGetParams(CamelModel):
    """Parameters for ``gridState.get``.

    Wire form::

        {"databaseId": "c:/.../file.db", "table": "contracts"}
    """

    database_id: str = Field(min_length=1)
    table: str = Field(min_length=1, max_length=128)


class GridStateSaveParams(CamelModel):
    """Parameters for ``gridState.save``.

    Wire form::

        {"databaseId": "c:/.../file.db", "table": "contracts",
         "state": {...}, "revision": "rev-3"}

    ``revision`` is the conflict token from the prior get/save; a mismatch
    means another session saved newer state and the caller must re-read.
    """

    database_id: str = Field(min_length=1)
    table: str = Field(min_length=1, max_length=128)
    state: GridState
    revision: str | None = None


class GridStateResult(CamelModel):
    """Result of ``gridState.get`` / ``gridState.save``.

    Wire form::

        {"state": {...}, "revision": "rev-3", "conflict": false}

    * ``state`` is the current persisted state (empty default on first get).
    * ``revision`` is the conflict token to carry on the next save.
    * ``conflict`` is true when a save was rejected because ``revision`` did
      not match the stored value; the caller must re-read ``state`` and retry.
    """

    state: GridState
    revision: str
    conflict: bool = False
