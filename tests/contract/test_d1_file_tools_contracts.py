"""D1 file-tools contract fixture tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.file_tools import (
    DirectusFileMetadata,
    JournalEntry,
    PresetPreviewResult,
)

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_contract_header() -> None:
    fixture = _load("table-d1-file-tools-contracts.json")
    assert fixture["contract"] == "table.d1.file-tools.fixtures.v1"


def test_directus_file_metadata_round_trip() -> None:
    fixture = _load("table-d1-file-tools-contracts.json")
    meta = DirectusFileMetadata.model_validate(fixture["directusFile"]["metadata"])
    assert meta.filename == "contract.pdf"
    assert meta.file_size == 18432


def test_preset_preview_round_trip() -> None:
    fixture = _load("table-d1-file-tools-contracts.json")
    result = PresetPreviewResult.model_validate(fixture["directusFile"]["presetPreview"]["result"])
    assert result.preset_key == "thumbnail-medium"
    assert "key=thumbnail-medium" in result.url


def test_journal_entry_round_trip() -> None:
    fixture = _load("table-d1-file-tools-contracts.json")
    entry = JournalEntry.model_validate(fixture["journal"]["entry"])
    assert entry.state == "rollback-required"
    assert entry.steps[0].backup_hash == "sha256-old"


def test_approved_asset_presets_do_not_include_arbitrary_keys() -> None:
    """Asset Preset previews only use approved keys (no arbitrary width/height)."""
    fixture = _load("table-d1-file-tools-contracts.json")
    presets = fixture["approvedAssetPresets"]
    for key in presets:
        assert "width" not in key
        assert "height" not in key
        assert "quality" not in key


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
