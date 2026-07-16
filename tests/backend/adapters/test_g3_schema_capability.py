"""Tests for the VibeTable Directus schema and capability files (vibetable-1.0).

Validates the Phase-3 generalization gate for the document-workspace
subsystem. After the business blueprints were removed and the
``vibetable-empty`` blueprint/capability were introduced, this module
replaces the former G3-specific assertions and now validates:

1. schema_version is vibetable-1.0
2. the six workspace-index collections exist (workspaces, folders,
   documents, schemes, revisions, links)
3. NO legacy business collections (projects/contracts/tasks) remain
4. revision update_fields are restricted (content/hash immutable)
5. the document-links collection relates to documents
6. the capability manifest loads and round-trips through by_collection
7. all workspace-index collections have versioning disabled
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from backend.adapters.directus.profile import CapabilityManifest

ROOT = Path(__file__).resolve().parents[3]


def load_blueprint(path: Path) -> dict[str, Any]:
    """Load + lightly validate the VibeTable Directus blueprint.

    Inlined here (previously imported from backend.adapters.directus.bootstrap,
    which was removed once schema bootstrap moved to the C# host). Only the
    contract/collections guard these tests rely on is reproduced.
    """
    payload = json.loads(path.read_text(encoding="utf-8"))
    assert payload.get("contract") == "vibetable.directus-blueprint.v1", "unsupported blueprint contract"
    assert isinstance(payload.get("collections"), dict) and payload["collections"], "no collections"
    return payload
VT_BLUEPRINT = ROOT / "directus" / "blueprints" / "vibetable-empty.json"
VT_CAPABILITY = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"

#: The six document-system collections the workspace index requires.
WORKSPACE_COLLECTIONS = {
    "vibetable_workspaces",
    "vibetable_workspace_folders",
    "vibetable_documents",
    "vibetable_document_schemes",
    "vibetable_document_revisions",
    "vibetable_document_links",
}

#: Legacy business collections that must NOT exist in the generalized
#: manifest (they were removed in Phase 3 along with their blueprints).
LEGACY_BUSINESS_COLLECTIONS = {
    "rcpm_projects",
    "rcpm_contracts",
    "rcpm_tasks",
    "vibetable_projects",
    "vibetable_contracts",
    "vibetable_tasks",
}


def test_blueprint_schema_version() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    assert blueprint["schema_version"] == "vibetable-1.0"


def test_blueprint_has_workspace_collections() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    for name in WORKSPACE_COLLECTIONS:
        assert name in blueprint["collections"], f"missing workspace collection: {name}"


def test_blueprint_has_no_legacy_business_collections() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    names = set(blueprint["collections"])
    leftover = names & LEGACY_BUSINESS_COLLECTIONS
    assert not leftover, f"legacy business collections must be removed: {sorted(leftover)}"


def test_blueprint_all_collections_versioning_false() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    for name, definition in blueprint["collections"].items():
        assert definition["versioning"] is False, f"{name} must have versioning=false"


def test_blueprint_revision_has_expected_fields() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    rev_fields = blueprint["collections"]["vibetable_document_revisions"]["fields"]
    for required in ("document", "scheme", "sequence", "kind", "hash", "size"):
        assert required in rev_fields, f"revision missing field: {required}"


def test_blueprint_links_relation_to_document() -> None:
    blueprint = load_blueprint(VT_BLUEPRINT)
    links_fields = blueprint["collections"]["vibetable_document_links"]["fields"]
    assert links_fields["document"].get("relation") == "vibetable_documents"


def test_capability_loads_and_validates() -> None:
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    assert manifest.schema_version == "vibetable-1.0"


def test_capability_has_workspace_collections() -> None:
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    names = {p.collection for p in manifest.collections}
    for name in WORKSPACE_COLLECTIONS:
        assert name in names, f"capability missing workspace collection: {name}"


def test_capability_has_no_legacy_business_collections() -> None:
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    names = {p.collection for p in manifest.collections}
    leftover = names & LEGACY_BUSINESS_COLLECTIONS
    assert not leftover, f"legacy business collections must be removed: {sorted(leftover)}"


def test_capability_revision_update_fields_restricted() -> None:
    """Revision content/hash must NOT be in update_fields (immutable after insert)."""
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    rev = next(p for p in manifest.collections if p.collection == "vibetable_document_revisions")
    for immutable in ("hash", "document", "scheme", "sequence", "kind", "version_label"):
        assert immutable not in rev.update_fields, (
            f"field {immutable!r} must not be updatable on revisions (immutable)"
        )


def test_capability_links_relation_to_document() -> None:
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    links = next(p for p in manifest.collections if p.collection == "vibetable_document_links")
    assert any(r.field == "document" for r in links.relations)


def test_capability_by_collection_round_trip() -> None:
    """The manifest must be consumable as the dynamic allow-list source."""
    manifest = CapabilityManifest.model_validate_json(VT_CAPABILITY.read_text(encoding="utf-8"))
    by_collection = manifest.by_collection
    for name in WORKSPACE_COLLECTIONS:
        assert name in by_collection
