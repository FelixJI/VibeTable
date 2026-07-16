from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.adapters.directus.profile import CollectionProfile


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
