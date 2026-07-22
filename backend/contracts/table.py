"""Shared column-schema contract for Directus-backed collection views.

Field aliases use ``camelCase`` (the language-neutral wire contract), while the
Python attribute names stay ``snake_case``. ``populate_by_name=True`` lets us
validate raw payloads that use either form — consistent with
:mod:`backend.contracts.system`.

``ColumnSchema`` is consumed by the Directus schema adapter and Directus RPC
contracts. The obsolete local-database open/list/read DTOs intentionally do
not live here: the WPF host owns its transport models and production reads go
through the Directus gateway.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict
from pydantic.alias_generators import to_camel


class CamelModel(BaseModel):
    """Shared base for table-domain contracts.

    Mirrors the camelCase / ``populate_by_name`` config used by
    :mod:`backend.contracts.system` so all backend DTOs serialize consistently.
    """

    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class ColumnSchema(CamelModel):
    """One Directus collection field in the grid-facing schema.

    Wire form::

        {"name": "amount", "title": "Amount",
         "dataType": "decimal", "editable": false, "nullable": true,
         "scale": 2, "precision": 10}
    """

    name: str
    title: str
    data_type: Literal[
        "text",
        "integer",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "time",
    ]
    editable: bool = False
    nullable: bool = True
    # Numeric precision/scale from Directus (``schema.numeric_precision`` /
    # ``schema.numeric_scale``). ``None`` for non-numeric fields or when Directus
    # does not report them. Decimal display precision and write-side scale
    # validation both key off ``scale``.
    scale: int | None = None
    precision: int | None = None
