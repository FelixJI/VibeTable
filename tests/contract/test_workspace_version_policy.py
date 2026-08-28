from __future__ import annotations

import hashlib
import json
import re
from copy import deepcopy
from pathlib import Path

import pytest

from backend._version import __version__ as app_version
from scripts.versioning import (
    is_complete_formal_release_evidence,
    validate_workspace_version_policy,
)
from tests.contract.test_product_contracts import SchemaMismatchError, _validate

ROOT = Path(__file__).resolve().parents[2]
POLICY = ROOT / "contracts" / "v2" / "workspace-version-policy.json"
SCHEMA = ROOT / "contracts" / "v2" / "workspace-version-policy.schema.json"
CORPUS = ROOT / "contracts" / "v2" / "compatibility-corpus.json"
ADR = ROOT / "docs" / "adr" / "0011-workspace-version-n-minus-one-policy.md"
SNAPSHOT_COORDINATOR = ROOT / "sidecar" / "internal" / "snapshot" / "coordinator.go"
SNAPSHOT_BUNDLE = ROOT / "sidecar" / "internal" / "snapshot" / "bundle.go"
SNAPSHOT_PACKAGE_HANDLERS = (
    ROOT / "sidecar" / "internal" / "workspacev2" / "snapshot_package_handlers.go"
)
WORKSPACE_LAYOUT = (
    ROOT / "desktop" / "src" / "VibeTable.Infrastructure" / "Workspace" / "WorkspaceLayout.cs"
)
CSHARP_WORKSPACE_CONTRACTS = (
    ROOT / "desktop" / "src" / "VibeTable.Contracts" / "WorkspaceV2Contracts.cs"
)
GO_WORKSPACE_CONTRACTS = ROOT / "sidecar" / "internal" / "contracts" / "v2" / "types.go"
GO_SNAPSHOT_VALIDATION = ROOT / "sidecar" / "internal" / "workspacedb" / "snapshot_validation.go"


def _complete_formal_release_evidence(root: Path, version: str) -> dict:
    artifacts = []
    for artifact_id, kind in (
        ("workspace", "workspace-archive"),
        ("snapshot", "snapshot-package"),
        ("invalid-workspace", "workspace-archive"),
    ):
        path = root / f"{artifact_id}.bin"
        path.write_bytes(artifact_id.encode())
        artifacts.append(
            {
                "id": artifact_id,
                "kind": kind,
                "path": path.name,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            }
        )
    return {
        "writerVersion": version,
        "sourceRelease": {
            "tag": f"v{version}",
            "sourceCommit": "a" * 40,
            "assetName": f"VibeTable-v{version}-win-x64.zip",
        },
        "artifacts": artifacts,
        "cases": [
            {"operation": "workspace.open", "artifactId": "workspace", "expected": "read"},
            {"operation": "snapshot.import", "artifactId": "snapshot", "expected": "migrate"},
            {
                "operation": "workspace.open",
                "artifactId": "invalid-workspace",
                "expected": "reject-zero-write",
            },
        ],
    }


def test_current_writer_derives_app_version_while_n_minus_one_evidence_is_frozen() -> None:
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    corpus = json.loads(CORPUS.read_text(encoding="utf-8"))

    _validate(policy, schema, schema)
    assert validate_workspace_version_policy(policy, corpus) == []
    assert policy["currentWriter"]["appVersion"] == app_version
    assert policy["currentWriter"] == {
        "appVersion": app_version,
        "workspaceManifestFormat": 2,
        "snapshotPackageFormat": 2,
        "internalSnapshotManifestFormat": 2,
    }
    assert policy["writerCompatibility"] == {
        "window": "current-and-one-previous-formal-release",
        "verificationGate": "disabled-until-packaged-runtime-evidence",
        "nMinusOneTarget": "0.5.0",
        "accepted": [{"appVersion": app_version, "status": "current"}],
        "pending": [
            {
                "appVersion": "0.5.0",
                "status": "pending",
                "compatibility": "unverified",
            }
        ],
    }
    assert policy["workspaceManifest"] == {
        "supportedFormat": 2,
        "topologySchemaVersion": 1,
        "businessSchemaVersion": 1,
        "olderFormatDisposition": "reject-zero-write-no-automatic-migration",
        "newerFormatDisposition": "reject-newer-zero-write",
    }
    assert policy["snapshotPackage"] == {
        "currentFormat": 2,
        "minimumSupportedFormat": 2,
        "unsupportedFormatDisposition": "reject-zero-write",
    }
    assert policy["compatibilityCorpus"] == {
        "mutationPolicy": "append-only",
        "immutablePrefix": {
            "anchorCommit": "b28a0fc3f0829ed9fd7c9b974daf41d350eba560",
            "path": "contracts/v2/compatibility-corpus.json",
            "baselineCount": 1,
            "formalReleaseCount": 0,
        },
    }


def test_policy_schema_rejects_unknown_fields_and_unverified_promotion() -> None:
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    policy = json.loads(POLICY.read_text(encoding="utf-8"))

    with_unknown = deepcopy(policy)
    with_unknown["compatibilityClaim"] = "v0.5.0"
    with pytest.raises(SchemaMismatchError):
        _validate(with_unknown, schema, schema)

    fabricated_verification = deepcopy(policy)
    fabricated_verification["writerCompatibility"]["pending"][0]["compatibility"] = "verified"
    with pytest.raises(SchemaMismatchError):
        _validate(fabricated_verification, schema, schema)


def test_policy_schema_requires_closed_disabled_verification_gate() -> None:
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    policy["writerCompatibility"]["verificationGate"] = "disabled-until-packaged-runtime-evidence"

    _validate(policy, schema, schema)

    missing = deepcopy(policy)
    missing["writerCompatibility"].pop("verificationGate")
    with pytest.raises(SchemaMismatchError):
        _validate(missing, schema, schema)

    enabled_without_evidence = deepcopy(policy)
    enabled_without_evidence["writerCompatibility"]["verificationGate"] = "enabled"
    with pytest.raises(SchemaMismatchError):
        _validate(enabled_without_evidence, schema, schema)


def test_verified_target_requires_complete_frozen_formal_release_evidence_but_stays_disabled(
    tmp_path: Path,
) -> None:
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    corpus = json.loads(CORPUS.read_text(encoding="utf-8"))
    target = policy["writerCompatibility"]["nMinusOneTarget"]
    policy["writerCompatibility"]["accepted"].append({"appVersion": target, "status": "verified"})
    policy["writerCompatibility"]["pending"] = []

    assert any(
        "formal release evidence is incomplete" in error
        for error in validate_workspace_version_policy(policy, corpus, tmp_path)
    )

    corpus["previousFormalReleases"] = [{"writerVersion": target}]
    assert any(
        "formal release evidence is incomplete" in error
        for error in validate_workspace_version_policy(policy, corpus, tmp_path)
    )

    policy["compatibilityCorpus"]["immutablePrefix"]["formalReleaseCount"] = 1
    assert any(
        "formal release evidence is incomplete" in error
        for error in validate_workspace_version_policy(policy, corpus, tmp_path)
    )

    evidence = _complete_formal_release_evidence(tmp_path, target)
    corpus["previousFormalReleases"] = [evidence]
    assert is_complete_formal_release_evidence(evidence, tmp_path)
    assert any(
        "workspace compatibility promotion is disabled" in error
        for error in validate_workspace_version_policy(policy, corpus, tmp_path)
    )


def test_formal_release_evidence_rejects_artifact_aliases_and_orphans(tmp_path: Path) -> None:
    version = "0.5.0"
    same_path = _complete_formal_release_evidence(tmp_path, version)
    same_path["artifacts"][2]["path"] = same_path["artifacts"][0]["path"]
    same_path["artifacts"][2]["sha256"] = same_path["artifacts"][0]["sha256"]
    assert not is_complete_formal_release_evidence(same_path, tmp_path)

    same_digest = _complete_formal_release_evidence(tmp_path, version)
    workspace_path = tmp_path / same_digest["artifacts"][0]["path"]
    rejected_path = tmp_path / same_digest["artifacts"][2]["path"]
    rejected_path.write_bytes(workspace_path.read_bytes())
    same_digest["artifacts"][2]["sha256"] = same_digest["artifacts"][0]["sha256"]
    assert not is_complete_formal_release_evidence(same_digest, tmp_path)

    orphan = _complete_formal_release_evidence(tmp_path, version)
    orphan_path = tmp_path / "orphan.bin"
    orphan_path.write_bytes(b"orphan")
    orphan["artifacts"].append(
        {
            "id": "orphan",
            "kind": "workspace-archive",
            "path": orphan_path.name,
            "sha256": hashlib.sha256(orphan_path.read_bytes()).hexdigest(),
        }
    )
    assert not is_complete_formal_release_evidence(orphan, tmp_path)


def test_formal_release_evidence_rejects_operation_artifact_kind_mismatch(
    tmp_path: Path,
) -> None:
    evidence = _complete_formal_release_evidence(tmp_path, "0.5.0")
    evidence["artifacts"][2]["kind"] = "snapshot-package"

    assert not is_complete_formal_release_evidence(evidence, tmp_path)


def test_internal_snapshot_manifest_policy_matches_runtime_writer_reader_and_export() -> None:
    coordinator = SNAPSHOT_COORDINATOR.read_text(encoding="utf-8")
    bundle = SNAPSHOT_BUNDLE.read_text(encoding="utf-8")
    export = SNAPSHOT_PACKAGE_HANDLERS.read_text(encoding="utf-8")
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    adr = " ".join(ADR.read_text(encoding="utf-8").split())

    writer = re.search(r"manifest := Manifest\{(?P<body>.*?)\n\s*\}", coordinator, re.DOTALL)
    assert writer is not None
    writer_format = re.search(r"FormatVersion:\s*(?P<version>\d+)", writer["body"])
    assert writer_format is not None
    reader_formats = re.findall(r"manifest\.FormatVersion\s*!=\s*(\d+)", bundle)

    assert "内部 snapshot manifest 当前格式也是 2" in adr
    assert "内部的 snapshot manifest 当前使用 `formatVersion: 1`" not in adr
    assert re.search(
        r'"snapshot/manifest\.json":\s+bundle\.Manifest\.Payload',
        export,
    )
    assert reader_formats == [writer_format["version"]]
    assert (
        policy["currentWriter"]["internalSnapshotManifestFormat"]
        == int(writer_format["version"])
        == 2
    )


def test_workspace_manifest_schema_versions_match_runtime_writer_and_readers() -> None:
    writer = WORKSPACE_LAYOUT.read_text(encoding="utf-8")
    csharp_reader = CSHARP_WORKSPACE_CONTRACTS.read_text(encoding="utf-8")
    go_reader = GO_WORKSPACE_CONTRACTS.read_text(encoding="utf-8")
    snapshot_reader = GO_SNAPSHOT_VALIDATION.read_text(encoding="utf-8")
    policy = json.loads(POLICY.read_text(encoding="utf-8"))

    topology_writer = re.search(r"TopologySchemaVersion\s*=\s*(\d+)", writer)
    business_writer = re.search(r"BusinessSchemaVersion\s*=\s*(\d+)", writer)
    business_reader = re.search(
        r"const SupportedBusinessSchemaVersion uint64 = (\d+)", snapshot_reader
    )

    assert topology_writer is not None
    assert business_writer is not None
    assert business_reader is not None
    assert "TopologySchemaVersion == 0 || BusinessSchemaVersion == 0" in csharp_reader
    assert re.search(
        r"value\.TopologySchemaVersion == 0\s*\|\|\s*value\.BusinessSchemaVersion == 0",
        go_reader,
    )
    assert (
        policy["workspaceManifest"]["topologySchemaVersion"] == int(topology_writer.group(1)) == 1
    )
    assert (
        policy["workspaceManifest"]["businessSchemaVersion"]
        == int(business_writer.group(1))
        == int(business_reader.group(1))
        == 1
    )


@pytest.mark.parametrize(
    ("mutate", "expected_error"),
    [
        (
            lambda policy: policy["writerCompatibility"]["accepted"].append(
                {"appVersion": "0.4.0", "status": "current"}
            ),
            "exactly one accepted current writer",
        ),
        (
            lambda policy: policy["writerCompatibility"]["accepted"][0].update(appVersion="0.5.0"),
            "accepted current writer must match currentWriter.appVersion",
        ),
        (
            lambda policy: policy["writerCompatibility"]["pending"].append(
                {
                    "appVersion": "0.4.0",
                    "status": "pending",
                    "compatibility": "unverified",
                }
            ),
            "at most one pending writer",
        ),
        (
            lambda policy: policy["writerCompatibility"]["pending"][0].update(appVersion="0.4.0"),
            "pending writer must be the nMinusOneTarget",
        ),
        (
            lambda policy: policy["writerCompatibility"]["pending"].clear(),
            "nMinusOneTarget must appear exactly once",
        ),
        (
            lambda policy: (
                policy["writerCompatibility"].update(nMinusOneTarget="9.0.0"),
                policy["writerCompatibility"]["pending"][0].update(appVersion="9.0.0"),
            ),
            "nMinusOneTarget must precede current writer",
        ),
        (
            lambda policy: policy["writerCompatibility"]["accepted"].append(
                {"appVersion": "0.5.0", "status": "verified"}
            ),
            "accepted and pending writer versions must be disjoint",
        ),
        (
            lambda policy: policy["writerCompatibility"]["accepted"].append(
                {"appVersion": "0.4.0", "status": "verified"}
            ),
            "writer compatibility versions must be exactly currentWriter and nMinusOneTarget",
        ),
        (
            lambda policy: policy["writerCompatibility"]["accepted"].append(
                {
                    "appVersion": policy["currentWriter"]["appVersion"],
                    "status": "verified",
                }
            ),
            "writer compatibility appVersion values must be unique",
        ),
        (
            lambda policy: policy["workspaceManifest"].update(supportedFormat=3),
            "workspace manifest format must match current writer",
        ),
        (
            lambda policy: policy["snapshotPackage"].update(currentFormat=3),
            "snapshot package format must match current writer",
        ),
        (
            lambda policy: policy["snapshotPackage"].update(minimumSupportedFormat=1),
            "snapshot package minimum must equal its only supported format",
        ),
    ],
)
def test_policy_semantic_validator_rejects_cross_field_contradictions(
    mutate,
    expected_error: str,
) -> None:
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    mutate(policy)

    assert any(expected_error in error for error in validate_workspace_version_policy(policy))
