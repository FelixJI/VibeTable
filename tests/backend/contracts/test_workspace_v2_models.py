from __future__ import annotations

import json
from pathlib import Path
from uuid import UUID

import pytest
from pydantic import ValidationError

from backend.contracts.workspace_v2 import (
    FileDocument,
    FileRevision,
    LeaseClaim,
    RetentionPolicy,
    RpcContractCatalog,
    SnapshotCatalogEntry,
    SnapshotManifest,
    SnapshotSeal,
    WorkspaceEvent,
    WorkspaceManifest,
    WorkspaceRegistryEntry,
    WorkspaceSession,
    WorkspaceWireScope,
    ensure_current_workspace_scope,
)

ROOT = Path(__file__).parents[3]
FIXTURES = ROOT / "contracts" / "v2" / "fixtures"

MODEL_BY_FIXTURE = {
    "workspace-manifest.json": WorkspaceManifest,
    "workspace-registry-entry.json": WorkspaceRegistryEntry,
    "workspace-session.json": WorkspaceSession,
    "file-document.json": FileDocument,
    "file-revision.json": FileRevision,
    "snapshot-manifest.json": SnapshotManifest,
    "snapshot-seal.json": SnapshotSeal,
    "snapshot-catalog-entry.json": SnapshotCatalogEntry,
    "lease-claim.json": LeaseClaim,
    "retention-policy.json": RetentionPolicy,
    "workspace-event.json": WorkspaceEvent,
    "rpc-catalog.json": RpcContractCatalog,
}


@pytest.mark.parametrize(("fixture_name", "model"), MODEL_BY_FIXTURE.items())
def test_v2_fixture_strictly_round_trips(fixture_name: str, model: type) -> None:
    payload = json.loads((FIXTURES / fixture_name).read_text(encoding="utf-8"))
    parsed = model.model_validate(payload)
    assert parsed.model_dump(mode="json", by_alias=True) == payload


@pytest.mark.parametrize(("fixture_name", "model"), MODEL_BY_FIXTURE.items())
def test_v2_models_reject_unknown_and_missing_fields(
    fixture_name: str,
    model: type,
) -> None:
    payload = json.loads((FIXTURES / fixture_name).read_text(encoding="utf-8"))
    payload["unexpected"] = True
    with pytest.raises(ValidationError):
        model.model_validate(payload)

    payload = json.loads((FIXTURES / fixture_name).read_text(encoding="utf-8"))
    payload.pop(next(iter(payload)))
    with pytest.raises(ValidationError):
        model.model_validate(payload)


@pytest.mark.parametrize(
    "mutation",
    ["untyped-items", "open-item", "missing-required"],
)
def test_rpc_catalog_rejects_unclosed_array_item_schemas(
    mutation: str,
) -> None:
    payload = json.loads((FIXTURES / "rpc-catalog.json").read_text(encoding="utf-8"))
    case = next(item for item in payload["rpcCases"] if item["method"] == "conflict.inspect")
    items = case["resultSchema"]["properties"]["items"]["items"]
    if mutation == "untyped-items":
        case["resultSchema"]["properties"]["items"]["items"] = {}
    elif mutation == "open-item":
        items["additionalProperties"] = True
    else:
        items["required"].remove("itemId")

    with pytest.raises(ValidationError, match="closed object"):
        RpcContractCatalog.model_validate(payload)


def test_file_revision_kind_invariants_are_closed() -> None:
    payload = json.loads((FIXTURES / "file-revision.json").read_text(encoding="utf-8"))
    payload["kind"] = "autosave"
    with pytest.raises(ValidationError, match="formal version"):
        FileRevision.model_validate(payload)

    payload = json.loads((FIXTURES / "file-revision.json").read_text(encoding="utf-8"))
    payload["restoredFromRevisionId"] = None
    with pytest.raises(ValidationError, match="restoredFromRevisionId"):
        FileRevision.model_validate(payload)

    payload = json.loads((FIXTURES / "file-revision.json").read_text(encoding="utf-8"))
    payload.update(
        revisionOrdinal=0,
        localSequence=7,
        formalVersion=None,
        kind="autosave",
        restoredFromRevisionId=None,
    )
    provisional = FileRevision.model_validate(payload)
    assert provisional.revision_ordinal == 0
    assert provisional.local_sequence == 7

    payload["localSequence"] = None
    with pytest.raises(ValidationError, match="localSequence"):
        FileRevision.model_validate(payload)

    payload["localSequence"] = 0
    with pytest.raises(ValidationError):
        FileRevision.model_validate(payload)

    payload["localSequence"] = 7
    payload["formalVersion"] = 3
    with pytest.raises(ValidationError, match="provisional"):
        FileRevision.model_validate(payload)

    payload.update(
        revisionOrdinal=4,
        localSequence=None,
        formalVersion=None,
        kind="formal",
    )
    with pytest.raises(ValidationError, match="formal version"):
        FileRevision.model_validate(payload)


def test_scope_rejects_late_workspace_epoch_and_sequence() -> None:
    scope = WorkspaceWireScope.model_validate(
        json.loads((FIXTURES / "workspace-event.json").read_text(encoding="utf-8"))["wire"]
    )
    ensure_current_workspace_scope(
        scope,
        workspace_id=UUID("11111111-1111-4111-8111-111111111111"),
        session_epoch=7,
        minimum_sequence=12,
    )
    with pytest.raises(ValueError, match="session_epoch_stale"):
        ensure_current_workspace_scope(
            scope,
            workspace_id=scope.workspace_id,
            session_epoch=8,
        )
    with pytest.raises(ValueError, match="sequence_stale"):
        ensure_current_workspace_scope(
            scope,
            workspace_id=scope.workspace_id,
            session_epoch=7,
            minimum_sequence=13,
        )
