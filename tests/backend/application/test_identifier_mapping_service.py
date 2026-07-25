"""Provider-neutral identifier value-object tests."""

from __future__ import annotations

import uuid

import pytest

from backend.application.identifier_mapping_service import (
    IdentifierMapping,
    IdentifierRegistry,
    normalize_display_name,
    stable_suffix,
)


class NoopMetadataPort:
    async def list_metadata(self, *_args: object, **_kwargs: object):
        return []

    async def upsert_metadata(self, *_args: object, **_kwargs: object):
        return {}

    async def delete_metadata(self, *_args: object, **_kwargs: object):
        return {}


def test_unicode_names_use_nfkc_casefold() -> None:
    assert normalize_display_name("  ＡＢＣ  ") == "abc"


def test_allocated_identifiers_are_ascii_and_stable_for_fixed_uuid() -> None:
    value = uuid.UUID("00000000-0000-0000-0000-000000000001")
    registry = IdentifierRegistry(NoopMetadataPort(), id_factory=lambda: value)

    assert registry.allocate_physical("collection") == "vt_t_0000000000000001"
    assert stable_suffix(1) == "0000000000000001"


def test_mapping_rejects_removed_provider_origin() -> None:
    mapping = IdentifierMapping(
        id="m1",
        entity_kind="collection",
        parent_physical_name=None,
        physical_name="vt_t_1",
        display_name="Orders",
        normalized_name="orders",
        origin="dire" "ctus",  # type: ignore[arg-type]
    )

    with pytest.raises(ValueError, match="origin"):
        mapping.item()
