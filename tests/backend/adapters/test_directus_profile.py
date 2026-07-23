from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.adapters.directus.profile import CollectionProfile, RelationProfile


def test_capability_hash_is_stable_and_changes_with_policy() -> None:
    first = CollectionProfile(
        collection="contracts",
        fields=["id", "status", "date_updated"],
        create_fields=["id", "status"],
        update_fields=["status"],
    )
    same = CollectionProfile.model_validate(first.model_dump())
    changed = first.model_copy(update={"allow_dashboards": True})

    assert first.capability_hash == same.capability_hash
    assert first.capability_hash != changed.capability_hash


def test_profile_rejects_unknown_capability_field() -> None:
    with pytest.raises(ValidationError, match="capability fields"):
        CollectionProfile(
            collection="contracts",
            fields=["id", "status"],
            create_fields=["id"],
            update_fields=["missing"],
            date_updated_field=None,
        )


def test_legacy_file_relation_normalizes_to_m2o_preset() -> None:
    relation = RelationProfile.model_validate(
        {
            "field": "document",
            "kind": "file",
            "related_collection": "directus_files",
        }
    )

    assert relation.kind == "m2o"
    assert relation.preset == "file"


def test_m2a_requires_allowed_collections_and_polymorphic_junction() -> None:
    with pytest.raises(ValidationError, match="polymorphic junction"):
        RelationProfile(
            field="sections",
            kind="m2a",
            allowed_collections=["headings"],
        )


def test_required_relation_rejects_nullify_delete_policy() -> None:
    with pytest.raises(ValidationError, match="required relations"):
        RelationProfile(
            field="contract",
            kind="m2o",
            related_collection="contracts",
            nullable=False,
            on_delete="nullify",
        )
