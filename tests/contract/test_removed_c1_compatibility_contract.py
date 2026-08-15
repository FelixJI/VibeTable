"""Deletion contracts for retired, never-registered C1 compatibility shapes."""

from __future__ import annotations

import importlib.util

import pytest
from pydantic import BaseModel, ValidationError

from backend.contracts.data_io import ApplyImportParams, ImportColumnMapping


def test_orphan_relation_projection_contract_is_not_importable() -> None:
    assert importlib.util.find_spec("backend.contracts.relation") is None


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (
            ImportColumnMapping,
            {
                "sourceColumn": "project",
                "targetField": "project",
                "relationLookup": False,
            },
        ),
        (
            ApplyImportParams,
            {
                "grantId": "grant-1",
                "collection": "orders",
                "token": "token-1",
                "chunkSize": 500,
            },
        ),
    ],
)
def test_retired_c1_wire_fields_are_rejected(
    model: type[BaseModel], payload: dict[str, object]
) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)


def test_identifier_mapping_module_is_not_importable() -> None:
    assert importlib.util.find_spec("backend.application.identifier_mapping_service") is None


def test_orphan_table_admin_contract_is_not_importable() -> None:
    assert importlib.util.find_spec("backend.contracts.table_admin") is None
