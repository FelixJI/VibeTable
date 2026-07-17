"""Contract-layer validation for ``table_admin`` params.

These tests guard the RPC boundary: a bad collection/field identifier must be
rejected here (pydantic ``ValidationError`` → JSON-RPC ``-32602 Invalid params``
with a clear message) rather than travel into the service and surface as the
dispatcher's opaque ``-32603 Internal error``. See the regression that motivated
this: non-ASCII / space / digit-first field names reached
``TableAdminService._profile_for`` and tripped ``CollectionProfile`` validation,
which is not a registered application error.

The identifier rule mirrors ``CollectionProfile.validate_fields`` in
``backend/adapters/directus/profile.py``: ASCII letter first, then letters,
digits, or underscores.
"""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.table_admin import CreateTableParams, FieldDefinition

#: Inputs that must be accepted (return of the validator is the normalised value).
_VALID_KEYS = ["a", "name", "firstName", "order_date", "A1", "x_1", "Ab_" * 20]
#: Inputs that must be rejected at the contract boundary with the identifier
#: hint. (Empty / over-length strings are rejected too, but by min/max_length
#: with a different message — those are covered separately below.)
_INVALID_KEYS = [
    "1bad",  # digit first
    "_underscore",  # underscore first
    "-dash",  # non-identifier char first
    "has space",  # space
    "has-dash",  # dash
    "has.dot",  # dot
    "姓名",  # non-ASCII (Chinese)
    "phone number",  # space + ascii
    "中文",  # non-ASCII
]


@pytest.mark.parametrize("key", _VALID_KEYS)
def test_field_definition_accepts_valid_identifier(key: str):
    field = FieldDefinition(key=key, type="string")
    assert field.key == key


@pytest.mark.parametrize("key", _INVALID_KEYS)
def test_field_definition_rejects_invalid_identifier(key: str):
    with pytest.raises(ValidationError) as exc_info:
        FieldDefinition(key=key, type="string")
    # The message must point at the identifier rule so the host can surface it.
    message = str(exc_info.value).lower()
    assert "must start with an ascii letter" in message


def test_field_definition_rejects_empty_and_overlength():
    """Length violations are a separate failure class (string_too_short /
    string_too_long), not the identifier rule. They still reject the call."""
    with pytest.raises(ValidationError):
        FieldDefinition(key="", type="string")
    with pytest.raises(ValidationError):
        FieldDefinition(key="a" * 65, type="string")


def test_field_definition_message_contains_offending_value():
    """The host surfaces ``ValidationError`` text to the user, so the bad name
    must appear in the message (otherwise the user cannot tell which field)."""
    with pytest.raises(ValidationError) as exc_info:
        FieldDefinition(key="姓名", type="string")
    assert "姓名" in str(exc_info.value)


@pytest.mark.parametrize("name", ["customers", "user_orders", "A1", "t1"])
def test_create_table_params_accepts_valid_name(name: str):
    params = CreateTableParams(name=name, fields=[])
    assert params.name == name


@pytest.mark.parametrize(
    "name", ["1bad", "_u", "has space", "用户订单", "a-b", "a.b"]
)
def test_create_table_params_rejects_invalid_name(name: str):
    with pytest.raises(ValidationError) as exc_info:
        CreateTableParams(name=name, fields=[])
    assert "must start with an ascii letter" in str(exc_info.value).lower()


def test_create_table_params_validates_each_field():
    """One bad field in a multi-field request must reject the whole call."""
    with pytest.raises(ValidationError) as exc_info:
        CreateTableParams(
            name="customers",
            fields=[
                FieldDefinition(key="ok", type="string"),
                FieldDefinition(key="bad name", type="string"),
            ],
        )
    assert "bad name" in str(exc_info.value)
