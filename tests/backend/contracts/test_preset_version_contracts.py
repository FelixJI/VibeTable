"""Contract guards for renderer-facing preset/content-version writes."""

import pytest
from pydantic import ValidationError

from backend.contracts.presets_versions_dashboards import (
    CreateVersionParams,
    DeletePresetParams,
    DeleteVersionParams,
    PromoteVersionParams,
    SavePresetParams,
    SaveVersionParams,
    VersionIdParams,
)


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (
            SavePresetParams,
            {
                "collection": "orders",
                "name": "My view",
                "view": {},
            },
        ),
        (
            DeletePresetParams,
            {"presetId": "p1", "expectedRevision": "rev-p1"},
        ),
        (
            CreateVersionParams,
            {"collection": "orders", "itemId": "row-1"},
        ),
        (
            SaveVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "values": {},
            },
        ),
        (
            PromoteVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "mainHash": "hash-1",
            },
        ),
        (
            DeleteVersionParams,
            {
                "collection": "orders",
                "itemId": "row-1",
                "versionId": "v1",
                "expectedRevision": "rev-v1",
            },
        ),
    ],
)
def test_write_contracts_require_operation_id(model: type, payload: dict) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)


def test_shared_version_id_contract_keeps_compare_read_only() -> None:
    params = VersionIdParams.model_validate(
        {"collection": "orders", "itemId": "row-1", "versionId": "v1"}
    )

    assert params.operation_id is None


def test_operation_id_uses_camel_case_on_the_wire() -> None:
    params = DeletePresetParams.model_validate(
        {
            "presetId": "p1",
            "expectedRevision": "rev-p1",
            "operationId": "op-delete-preset-1",
        }
    )

    assert params.model_dump(by_alias=True) == {
        "presetId": "p1",
        "expectedRevision": "rev-p1",
        "operationId": "op-delete-preset-1",
    }
