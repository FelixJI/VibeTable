"""Server-side value coercion/validation for Directus writes.

The inline edit path (``directus.update``/``directus.create``) and the paste
path (``table.applyPaste``) both forward user values to Directus without a
type check. A decimal column silently truncates excess fractional digits at
the database boundary, which is the exact "integer cents vs. float drift"
footgun this module exists to close.

:func:`validate_number_field` is a PURE check: it raises
:class:`~backend.adapters.directus.errors.DirectusSchemaError` when a numeric
value exceeds the column's declared ``scale`` (fractional digits) or
``precision`` (significant digits), or carries a fractional part for an
integer column. It never mutates the value — callers decide whether to
truncate, reject, or surface a diagnostic. Non-numeric data types and
non-finite/unparseable input are left to the database (a 422 from Directus is
the correct outcome for ``"abc"`` in a number column).
"""

from __future__ import annotations

import math
from decimal import Decimal, InvalidOperation
from typing import Any

from backend.adapters.directus.errors import DirectusSchemaError

#: Numeric data types the grid layer exposes (mirrors ``ColumnSchema.data_type``).
_NUMERIC_DATA_TYPES = {"integer", "decimal"}


def validate_number_field(
    value: Any,
    *,
    data_type: str | None,
    scale: int | None,
    precision: int | None,
    field_name: str,
) -> None:
    """Reject numeric input that exceeds the column's declared precision/scale.

    Parameters mirror :class:`~backend.contracts.table.ColumnSchema`. When
    ``data_type`` is not numeric, or when neither ``scale`` nor ``precision``
    is declared and the type is decimal, this is a no-op (backward compatible
    with columns Directus reports no precision for). Integer columns always
    reject a fractional part.

    Raises :class:`DirectusSchemaError` on a violation.
    """
    if data_type not in _NUMERIC_DATA_TYPES:
        return
    if value is None:
        return

    # Normalize to Decimal without going through float (which would already
    # introduce binary rounding). Strings are parsed directly; numbers via
    # their repr. Unparseable input is the database's problem, not ours.
    try:
        decimal = _to_decimal(value)
    except _UnparseableError:
        return
    if decimal is None or not decimal.is_finite():
        return

    # Fractional digit count, ignoring trailing zeros (Decimal("3.00") is a
    # whole number with 0 fractional digits). `normalize()` strips trailing
    # zeros so the exponent reflects the meaningful precision only.
    normalized = decimal.normalize()
    exponent = normalized.as_tuple().exponent
    frac_digits = -exponent if isinstance(exponent, int) and exponent < 0 else 0
    has_fraction = frac_digits > 0
    # For scale/precision checks, however, the user-typed precision matters:
    # "3.00" declares 2 fractional places even though the value is whole. Use
    # the ORIGINAL decimal's exponent for scale counting (what the user typed),
    # but `has_fraction` (value-level) for the integer guard.
    typed_exponent = decimal.as_tuple().exponent
    typed_frac_digits = (
        -typed_exponent if isinstance(typed_exponent, int) and typed_exponent < 0 else 0
    )

    if data_type == "integer" and has_fraction:
        raise DirectusSchemaError(
            f"field {field_name!r} is an integer; fractional digits are not allowed"
        )

    if scale is not None and typed_frac_digits > scale:
        hint = (
            "this column does not allow fractional digits"
            if scale == 0
            else f"this column allows at most {scale} fractional digit{'s' if scale != 1 else ''}"
        )
        raise DirectusSchemaError(f"field {field_name!r}: {hint}")

    if precision is not None:
        # significant digits = adjusted exponent tracks the position of the
        # most significant digit; total count = digits - leading-zero slack.
        digits = len(decimal.as_tuple().digits)
        if digits > precision:
            raise DirectusSchemaError(
                f"field {field_name!r}: this column allows at most {precision} significant digits"
            )


class _UnparseableError(Exception):
    """Internal sentinel: the value is not a number we can reason about."""


def _to_decimal(value: Any) -> Decimal | None:
    """Best-effort conversion to a finite :class:`Decimal`, else raise."""
    if isinstance(value, bool):
        # bool is an int subclass; treat as the integer 0/1.
        return Decimal(int(value))
    if isinstance(value, Decimal):
        return value
    if isinstance(value, int):
        return Decimal(value)
    if isinstance(value, float):
        if not math.isfinite(value):
            return None
        # repr round-trips the shortest faithful decimal expansion.
        return Decimal(repr(value))
    if isinstance(value, str):
        text = value.strip()
        if not text:
            return None
        try:
            return Decimal(text)
        except InvalidOperation as exc:
            raise _UnparseableError from exc
    raise _UnparseableError
