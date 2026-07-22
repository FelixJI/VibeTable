"""Tests for the numeric scale/precision write-side validator."""

from __future__ import annotations

from decimal import Decimal

import pytest

from backend.adapters.directus.coerce import validate_number_field
from backend.adapters.directus.errors import DirectusSchemaError

# ---------------------------------------------------------------------------
# No-op cases (backward compatible — nothing to validate)
# ---------------------------------------------------------------------------


def test_non_numeric_data_type_is_a_noop() -> None:
    # A text field never raises, regardless of the value.
    validate_number_field(
        "anything",
        data_type="text",
        scale=None,
        precision=None,
        field_name="name",
    )


def test_none_value_is_a_noop() -> None:
    validate_number_field(
        None,
        data_type="decimal",
        scale=2,
        precision=10,
        field_name="amount",
    )


def test_decimal_without_declared_scale_is_unconstrained() -> None:
    # Legacy columns Directus reports no precision for: accept anything.
    validate_number_field(
        "3.1415926535",
        data_type="decimal",
        scale=None,
        precision=None,
        field_name="amount",
    )


# ---------------------------------------------------------------------------
# Integer storage
# ---------------------------------------------------------------------------


def test_integer_rejects_fractional_part() -> None:
    with pytest.raises(DirectusSchemaError, match="integer"):
        validate_number_field(
            "3.7",
            data_type="integer",
            scale=None,
            precision=None,
            field_name="qty",
        )


def test_integer_accepts_whole_number() -> None:
    validate_number_field(
        "3",
        data_type="integer",
        scale=None,
        precision=None,
        field_name="qty",
    )
    # Decimal("3.00") is whole too.
    validate_number_field(
        Decimal("3.00"),
        data_type="integer",
        scale=None,
        precision=None,
        field_name="qty",
    )


# ---------------------------------------------------------------------------
# Decimal scale
# ---------------------------------------------------------------------------


def test_decimal_rejects_excess_fractional_digits() -> None:
    with pytest.raises(DirectusSchemaError, match="2 fractional"):
        validate_number_field(
            "3.14159",
            data_type="decimal",
            scale=2,
            precision=10,
            field_name="amount",
        )


def test_decimal_accepts_value_within_scale() -> None:
    validate_number_field(
        "3.14",
        data_type="decimal",
        scale=2,
        precision=10,
        field_name="amount",
    )


def test_decimal_scale_zero_blocks_all_fractional_digits() -> None:
    validate_number_field(
        "3",
        data_type="decimal",
        scale=0,
        precision=10,
        field_name="amount",
    )
    with pytest.raises(DirectusSchemaError, match="fractional"):
        validate_number_field(
            "3.5",
            data_type="decimal",
            scale=0,
            precision=10,
            field_name="amount",
        )


# ---------------------------------------------------------------------------
# Precision
# ---------------------------------------------------------------------------


def test_precision_rejects_too_many_significant_digits() -> None:
    # 4 significant digits allowed: 12.34 ok, 123.45 (5 digits) rejected.
    validate_number_field(
        "12.34",
        data_type="decimal",
        scale=2,
        precision=4,
        field_name="amount",
    )
    with pytest.raises(DirectusSchemaError, match="significant digits"):
        validate_number_field(
            "123.45",
            data_type="decimal",
            scale=2,
            precision=4,
            field_name="amount",
        )


# ---------------------------------------------------------------------------
# Input shapes
# ---------------------------------------------------------------------------


def test_accepts_float_int_decimal_and_string_inputs() -> None:
    # All four numeric input shapes are handled without going through float
    # (which would itself introduce rounding).
    validate_number_field(3.14, data_type="decimal", scale=2, precision=10, field_name="a")
    validate_number_field(3, data_type="decimal", scale=2, precision=10, field_name="a")
    validate_number_field(Decimal("3.14"), data_type="decimal", scale=2, precision=10, field_name="a")
    validate_number_field("3.14", data_type="decimal", scale=2, precision=10, field_name="a")


def test_unparseable_string_is_left_to_the_database() -> None:
    # "abc" is not a number we can reason about; reject is the DB's job (422).
    validate_number_field(
        "abc",
        data_type="decimal",
        scale=2,
        precision=10,
        field_name="amount",
    )


def test_negative_value_counts_digits_correctly() -> None:
    # The sign must not be counted as a digit.
    validate_number_field(
        "-3.14",
        data_type="decimal",
        scale=2,
        precision=4,
        field_name="amount",
    )
    with pytest.raises(DirectusSchemaError, match="fractional"):
        validate_number_field(
            "-3.141",
            data_type="decimal",
            scale=2,
            precision=4,
            field_name="amount",
        )
