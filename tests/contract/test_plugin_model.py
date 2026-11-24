from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.plugin import PluginManifest
from backend.contracts.plugin_model import PluginModelDeclaration


def model_manifest() -> dict:
    root = Path(__file__).resolve().parents[2]
    return json.loads(
        (root / "examples/plugins/model-conformance/manifest.json").read_text(encoding="utf-8")
    )


def test_archive_policy_is_checked_at_manifest_boundary() -> None:
    model = model_manifest()["permissions"]["model"]
    entity = model["entities"][0]
    entity["operations"] = ["read", "query", "update"]
    file = {"maxFiles": 1, "maxBytesPerFile": 32 << 20, "allowedMimeTypes": ["image/png"]}
    archive = {"key": "issued", "label": "签发归档", "type": "file", "file": file}
    entity["fields"].append(archive)
    assert PluginModelDeclaration.model_validate(model)
    for patch in [
        {"maxFiles": 0},
        {"maxFiles": 2},
        {"maxBytesPerFile": 0},
        {"maxBytesPerFile": (32 << 20) + 1},
        {"allowedMimeTypes": []},
        {"allowedMimeTypes": ["image/*"]},
        {"allowedMimeTypes": ["image/png", "image/jpeg"]},
        {"protected": False},
    ]:
        archive["file"] = {**file, **patch}
        with pytest.raises(ValidationError):
            PluginModelDeclaration.model_validate(model)
    archive["file"] = file
    entity["operations"].remove("update")
    with pytest.raises(ValidationError):
        PluginModelDeclaration.model_validate(model)


def test_native_manifest_accepts_closed_symmetric_model() -> None:
    manifest = model_manifest()
    parsed = PluginManifest.model_validate(manifest)
    assert parsed.permissions["model"] == manifest["permissions"]["model"]


@pytest.mark.parametrize("case", ["contract", "precision", "identity", "reciprocal", "operation"])
def test_model_rejects_unknown_or_unsafe_declarations(case: str) -> None:
    model = copy.deepcopy(model_manifest()["permissions"]["model"])
    if case == "contract":
        model["contract"] = "unknown"
    elif case == "precision":
        model["entities"][2]["fields"][1]["precision"] = "decimal"
    elif case == "identity":
        model["entities"][0]["tableId"] = "tbl_user_data"
    elif case == "reciprocal":
        model["entities"][0]["fields"][1]["relation"]["field"] = "missing"
    else:
        model["entities"][0]["operations"].append("sql")
    with pytest.raises(ValidationError):
        PluginModelDeclaration.model_validate(model)
