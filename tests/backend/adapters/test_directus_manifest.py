from __future__ import annotations

import json
from pathlib import Path

from backend.adapters.directus.profile import CapabilityManifest

ROOT = Path(__file__).resolve().parents[3]

#: The six document-system collections the empty VibeTable manifest exposes.
WORKSPACE_COLLECTIONS = {
    "vibetable_workspaces",
    "vibetable_workspace_folders",
    "vibetable_documents",
    "vibetable_document_schemes",
    "vibetable_document_revisions",
    "vibetable_document_links",
}


def test_empty_capability_manifest_is_valid_and_has_workspace_collections() -> None:
    path = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"
    manifest = CapabilityManifest.model_validate_json(path.read_text(encoding="utf-8"))

    assert manifest.schema_version == "vibetable-1.0"
    assert set(manifest.by_collection) == WORKSPACE_COLLECTIONS
    assert len(manifest.by_collection) == len(manifest.collections)
    assert "shares" in manifest.disabled_features
    assert all(profile.primary_key == "id" for profile in manifest.collections)


def test_blueprint_relations_reference_declared_or_directus_collections() -> None:
    path = ROOT / "directus" / "blueprints" / "vibetable-empty.json"
    blueprint = json.loads(path.read_text(encoding="utf-8"))
    collections = set(blueprint["collections"])
    allowed_targets = collections | {"directus_users", "directus_files"}

    for collection in blueprint["collections"].values():
        assert collection["fields"]["id"] == {"type": "uuid", "primary_key": True}
        for field in collection["fields"].values():
            target = field.get("relation")
            if target is not None:
                assert target in allowed_targets

    assert blueprint["disabled"] == [
        "shares",
        "translations",
        "external_sso",
        "share_email_flow",
    ]
