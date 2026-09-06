from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tomllib
import zipfile
from pathlib import Path
from types import SimpleNamespace
from typing import cast

import pytest

from qa import fault_injection, package_check, release_candidate
from qa.package_check import check_package
from scripts import build_next
from scripts.qa.windows_process_scope import ProcessScopeQueryError, WindowsProcessScope
from scripts.release import (
    _ensure_clean_worktree,
    activate_upgrade,
    prepare_upgrade,
)
from scripts.versioning import (
    VersionError,
    bump_version,
    check_release_dependency_versions,
    check_versions,
    collect_release_versions,
    collect_versions,
    read_project_version,
    update_versions,
    validate_version,
)

REPO_ROOT = Path(__file__).resolve().parent.parent


def _write_recovery_tools(paths: build_next.RepoPaths) -> None:
    for tool in (paths.kopia_binary, paths.age_binary, paths.age_keygen_binary):
        tool.parent.mkdir(parents=True, exist_ok=True)
        # A structurally valid Windows amd64 PE fixture keeps package tests from
        # accepting arbitrary bytes. Runtime/Go metadata are injected below;
        # the real release gate executes and inspects the actual binaries.
        payload = bytearray(512)
        payload[:2] = b"MZ"
        payload[0x3C:0x40] = (0x80).to_bytes(4, "little")
        payload[0x80:0x84] = b"PE\0\0"
        payload[0x84:0x86] = (0x8664).to_bytes(2, "little")
        tool.write_bytes(payload)


def _version_tuple(value: str) -> tuple[int, ...]:
    """Parse a MAJOR.MINOR.PATCH version into a comparable tuple."""
    return tuple(int(part) for part in validate_version(value).split("."))


def _release_build_info() -> dict[str, str]:
    versions = collect_release_versions(REPO_ROOT)
    return {
        "version": versions.app,
        "pocketBaseVersion": versions.pocketbase,
        "celVersion": versions.cel,
        "contractVersion": versions.contract,
        "schemaVersion": versions.schema,
        "migrationHash": versions.migration_hash,
        "protocolV2Version": "2.0",
        "workspaceFormat": "2",
        "repositoryFormat": "kopia-v3",
        "snapshotFormat": "2",
        "packageFormat": "2",
        "kopiaVersion": build_next.KOPIA_VERSION,
        "ageVersion": build_next.AGE_VERSION,
    }


def _release_modules(
    license_dir: Path,
    *modules: tuple[str, str],
) -> list[dict[str, str]]:
    return [
        {
            "path": name,
            "version": version,
            "license": "MPL-2.0",
            "dir": str(license_dir),
        }
        for name, version in (
            *modules,
            ("github.com/kopia/kopia", build_next.KOPIA_VERSION),
            ("filippo.io/age", build_next.AGE_VERSION),
        )
    ]


def test_repository_versions_are_consistent() -> None:
    assert check_versions(REPO_ROOT) == []
    versions = collect_release_versions(REPO_ROOT)
    assert versions.pocketbase == "0.40.1"
    assert versions.cel == "0.31.0"
    assert versions.contract == "v1"
    assert versions.schema == "11"
    assert len(versions.migration_hash) == 64


def test_release_dependency_versions_fail_fast_before_packaging(tmp_path: Path) -> None:
    for relative in (
        Path("backend/_version.py"),
        Path("sidecar/go.mod"),
        Path("sidecar/internal/buildinfo/info.go"),
        Path("sidecar/migrations/manifest.json"),
    ):
        target = tmp_path / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(REPO_ROOT / relative, target)

    assert check_release_dependency_versions(tmp_path) == []
    go_mod = tmp_path / "sidecar/go.mod"
    go_mod.write_text(
        go_mod.read_text(encoding="utf-8").replace(
            "github.com/google/cel-go v0.31.0",
            "github.com/google/cel-go v0.26.1",
        ),
        encoding="utf-8",
    )

    assert check_release_dependency_versions(tmp_path) == [
        "sidecar go.mod dependency version mismatch: github.com/google/cel-go "
        "(expected v0.31.0, got v0.26.1)"
    ]


@pytest.mark.parametrize(
    ("part", "expected"),
    [("major", "2.0.0"), ("minor", "1.3.0"), ("patch", "1.2.4")],
)
def test_semver_bump(part: str, expected: str) -> None:
    assert bump_version("1.2.3", part) == expected


def test_version_dry_run_no_longer_targets_provider_extensions() -> None:
    removed_provider = "".join(["di", "rectus"])
    original = read_project_version(REPO_ROOT)
    changed = update_versions(REPO_ROOT, "9.8.7", dry_run=True)
    relative = {path.relative_to(REPO_ROOT).as_posix() for path in changed}
    assert relative == {
        "backend/_version.py",
        "contracts/v2/workspace-version-policy.json",
        "desktop/publish-layout.json",
    }
    assert all(removed_provider not in item.lower() for item in relative)
    assert read_project_version(REPO_ROOT) == original


def test_version_update_derives_workspace_policy_without_claiming_n_minus_one(
    tmp_path: Path,
) -> None:
    for relative in (
        Path("backend/_version.py"),
        Path("contracts/v2/compatibility-corpus.json"),
        Path("contracts/v2/workspace-version-policy.json"),
        Path("contracts/v2/workspace-version-policy.schema.json"),
        Path("desktop/publish-layout.json"),
    ):
        target = tmp_path / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(REPO_ROOT / relative, target)

    changed = update_versions(tmp_path, "0.5.2")
    policy = json.loads(
        (tmp_path / "contracts/v2/workspace-version-policy.json").read_text(encoding="utf-8")
    )

    assert {path.relative_to(tmp_path).as_posix() for path in changed} == {
        "backend/_version.py",
        "contracts/v2/workspace-version-policy.json",
        "desktop/publish-layout.json",
    }
    assert policy["currentWriter"]["appVersion"] == "0.5.2"
    assert policy["writerCompatibility"] == {
        "window": "current-and-one-previous-formal-release",
        "verificationGate": "disabled-until-packaged-runtime-evidence",
        "nMinusOneTarget": "0.5.0",
        "accepted": [{"appVersion": "0.5.2", "status": "current"}],
        "pending": [
            {
                "appVersion": "0.5.0",
                "status": "pending",
                "compatibility": "unverified",
            }
        ],
    }


def test_version_update_rejects_verified_state_while_runtime_evidence_gate_is_disabled(
    tmp_path: Path,
) -> None:
    for relative in (
        Path("backend/_version.py"),
        Path("contracts/v2/compatibility-corpus.json"),
        Path("contracts/v2/workspace-version-policy.json"),
        Path("contracts/v2/workspace-version-policy.schema.json"),
        Path("desktop/publish-layout.json"),
    ):
        target = tmp_path / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(REPO_ROOT / relative, target)
    policy_path = tmp_path / "contracts/v2/workspace-version-policy.json"
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["writerCompatibility"]["accepted"] = [
        {"appVersion": "0.5.1", "status": "current"},
        {"appVersion": "0.5.0", "status": "verified"},
    ]
    policy["writerCompatibility"]["pending"] = []
    policy["compatibilityCorpus"]["immutablePrefix"]["formalReleaseCount"] = 1
    policy_path.write_text(json.dumps(policy), encoding="utf-8")
    corpus_path = tmp_path / "contracts/v2/compatibility-corpus.json"
    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
    artifacts = []
    for artifact_id, kind in (
        ("workspace", "workspace-archive"),
        ("snapshot", "snapshot-package"),
        ("invalid", "workspace-archive"),
    ):
        artifact_path = corpus_path.parent / f"{artifact_id}.bin"
        artifact_path.write_bytes(artifact_id.encode())
        artifacts.append(
            {
                "id": artifact_id,
                "kind": kind,
                "path": artifact_path.name,
                "sha256": hashlib.sha256(artifact_path.read_bytes()).hexdigest(),
            }
        )
    corpus["previousFormalReleases"] = [
        {
            "writerVersion": "0.5.0",
            "sourceRelease": {
                "tag": "v0.5.0",
                "sourceCommit": "a" * 40,
                "assetName": "VibeTable-v0.5.0-win-x64.zip",
            },
            "artifacts": artifacts,
            "cases": [
                {"operation": "workspace.open", "artifactId": "workspace", "expected": "read"},
                {"operation": "snapshot.import", "artifactId": "snapshot", "expected": "migrate"},
                {
                    "operation": "workspace.open",
                    "artifactId": "invalid",
                    "expected": "reject-zero-write",
                },
            ],
        }
    ]
    corpus_path.write_text(json.dumps(corpus), encoding="utf-8")

    with pytest.raises(VersionError, match="promotion is disabled"):
        update_versions(tmp_path, "0.5.2")

    assert read_project_version(tmp_path) == "0.5.1"
    assert (
        json.loads(policy_path.read_text(encoding="utf-8"))["writerCompatibility"][
            "verificationGate"
        ]
        == "disabled-until-packaged-runtime-evidence"
    )


def test_version_snapshot_detects_workspace_policy_current_writer_drift(
    tmp_path: Path,
) -> None:
    for relative in (
        Path("backend/_version.py"),
        Path("contracts/v2/workspace-version-policy.json"),
        Path("desktop/publish-layout.json"),
    ):
        target = tmp_path / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(REPO_ROOT / relative, target)
    policy_path = tmp_path / "contracts/v2/workspace-version-policy.json"
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["currentWriter"]["appVersion"] = "0.5.0"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")

    assert collect_versions(tmp_path).mismatches == {"workspace policy current writer": "0.5.0"}


def test_backend_bundle_omits_removed_directus_websocket_runtime() -> None:
    pyproject = tomllib.loads((REPO_ROOT / "pyproject.toml").read_text(encoding="utf-8"))

    assert all(
        not dependency.startswith("websockets")
        for dependency in pyproject["project"]["dependencies"]
    )
    assert "websockets" not in build_next.BACKEND_HIDDEN_IMPORTS


def test_application_version_has_one_editable_source() -> None:
    pyproject = tomllib.loads((REPO_ROOT / "pyproject.toml").read_text(encoding="utf-8"))
    package = json.loads((REPO_ROOT / "desktop/web-grid/package.json").read_text(encoding="utf-8"))
    lock = json.loads(
        (REPO_ROOT / "desktop/web-grid/package-lock.json").read_text(encoding="utf-8")
    )
    props = (REPO_ROOT / "desktop/Directory.Build.props").read_text(encoding="utf-8")
    with (REPO_ROOT / "uv.lock").open("rb") as stream:
        uv_lock = tomllib.load(stream)

    assert "version" not in pyproject["project"]
    assert pyproject["project"]["dynamic"] == ["version"]
    assert pyproject["tool"]["setuptools"]["dynamic"]["version"] == {
        "attr": "backend._version.__version__"
    }
    assert "version" not in package
    assert "version" not in lock
    assert "version" not in lock["packages"][""]
    editable_package = next(
        package
        for package in uv_lock["package"]
        if package["name"] == "vibetable" and package.get("source", {}).get("editable") == "."
    )
    assert "version" not in editable_package
    assert r"backend\_version.py" in props
    assert read_project_version(REPO_ROOT) == collect_release_versions(REPO_ROOT).app


def test_manifest_contains_sidecar_release_identity_and_no_runtime_installer() -> None:
    paths = build_next.RepoPaths.default(REPO_ROOT)
    digest = "a" * 64
    manifest = json.loads(build_next.render_manifest(paths, sidecar_sha256=digest))
    version = read_project_version(REPO_ROOT)

    assert manifest["components"]["host"] == {"version": version}
    assert manifest["components"]["backend"] == {"version": version}
    assert manifest["components"]["web"] == {"version": version}
    assert manifest["components"]["sidecar"] == {
        "version": version,
        "pocketBaseVersion": "0.40.1",
        "celVersion": "0.31.0",
        "contractVersion": "2.0",
        "schemaVersion": "11",
        "migrationHash": collect_release_versions(REPO_ROOT).migration_hash,
        "sha256": digest,
    }
    assert manifest["launch"] == {
        "host": "VibeTable.Next.exe",
        "backend": "resources/backend/vibetable-backend.exe",
        "webGrid": "resources/web-grid",
        "sidecar": "resources/sidecar/vibetable-pb.exe",
        "previewHost": "VibeTable.Next.exe",
    }
    assert manifest["assets"]["migrations"] == "resources/sidecar/migrations/manifest.json"
    assert manifest["assets"]["sbom"] == "resources/sidecar/sbom.cdx.json"
    assert manifest["data"] == {
        "shellRoot": "%LOCALAPPDATA%/VibeTable/shell",
        "managedWorkspaceRoot": ("%LOCALAPPDATA%/VibeTable/shell/workspaces/<workspaceId>"),
        "mirroredActivityRoot": ("%LOCALAPPDATA%/VibeTable/activity/<workspaceId>"),
        "workspaceIdentity": "manifest-uuid",
        "preserveOnUninstall": True,
    }
    assert manifest["assets"]["recoveryGuide"] == "resources/RECOVERY.md"
    assert manifest["assets"]["workspaceContracts"] == "resources/contracts/v2"
    assert manifest["assets"]["recoveryTools"] == {
        "kopia": "resources/sidecar/tools/kopia.exe",
        "age": "resources/sidecar/tools/age.exe",
        "ageKeygen": "resources/sidecar/tools/age-keygen.exe",
    }
    assert manifest["formats"] == {
        "workspace": 2,
        "repository": "kopia-v3",
        "snapshot": 2,
        "package": 2,
        "contracts": "2.0",
    }
    encoded = json.dumps(manifest).lower()
    assert "".join(["di", "rectus"]) not in encoded
    assert "node_modules" not in encoded
    assert "npm" not in encoded


def test_desktop_publish_is_single_file_without_satellite_language_directories() -> None:
    paths = build_next.RepoPaths.default(REPO_ROOT)
    command = build_next.build_dotnet_publish_command(paths)

    assert "-p:PublishSingleFile=true" in command
    assert "-p:IncludeNativeLibrariesForSelfExtract=true" in command
    assert "-p:EnableCompressionInSingleFile=true" in command
    assert "-p:DebugSymbols=false" in command
    assert "-p:SatelliteResourceLanguages=en-US" in command


@pytest.mark.parametrize(
    ("release", "expected"),
    [(False, ["swap"]), (True, ["smoke", "swap"])],
)
def test_release_build_runs_self_update_smoke_before_atomic_publish(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    release: bool,
    expected: list[str],
) -> None:
    defaults = build_next.RepoPaths.default(REPO_ROOT)
    paths = defaults.with_output_roots(
        staging_root=tmp_path / "staging",
        scratch_root=tmp_path / "scratch",
        publish_root=tmp_path / "publish",
    )
    events: list[str] = []

    monkeypatch.setattr(build_next, "check_versions", lambda _root: [])
    monkeypatch.setattr(build_next, "_build_sidecar", lambda _paths, *, skip: None)
    monkeypatch.setattr(build_next, "_build_web", lambda _paths, *, skip: None)
    monkeypatch.setattr(build_next, "_build_backend", lambda _paths, *, skip: None)

    def build_desktop(stage: build_next.RepoPaths, *, skip: bool) -> None:
        assert not skip
        stage.host_exe.parent.mkdir(parents=True, exist_ok=True)
        stage.host_exe.write_bytes(b"published-host")

    monkeypatch.setattr(build_next, "_build_desktop", build_desktop)
    monkeypatch.setattr(build_next, "verify_sidecar_package", lambda _paths: None)
    monkeypatch.setattr(build_next, "stage_workspace_contracts", lambda _paths: None)
    monkeypatch.setattr(build_next, "write_manifest", lambda _paths: None)
    monkeypatch.setattr(build_next, "write_release_manifest", lambda _paths: None)

    def smoke(package: Path, root: Path, *, repo_root: Path) -> None:
        assert package == paths.staging_root
        assert root == REPO_ROOT / "build" / "self-update-smoke"
        assert repo_root == REPO_ROOT
        events.append("smoke")

    monkeypatch.setattr(build_next, "run_desktop_self_update_smoke", smoke)
    monkeypatch.setattr(
        build_next,
        "_atomic_swap",
        lambda _staging, _publish: events.append("swap"),
    )
    args = build_next.parse_args(["--release"] if release else [])

    assert build_next.run_build(paths, args) == 0
    assert events == expected


def test_self_update_activation_pointer_requires_exact_prepared_identity(
    tmp_path: Path,
) -> None:
    target = tmp_path / "VibeTable.Next"
    stage = tmp_path / ".VibeTable.Next.update-smoke"
    pointer = tmp_path / build_next.PENDING_UPDATE_ACTIVATION_POINTER
    token = "a" * 64
    pointer.write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "state": "prepared",
                "targetRoot": str(target),
                "stagingRoot": str(stage),
                "currentVersion": "1.0.0",
                "targetVersion": "1.0.1",
                "token": token,
                "smokeTest": True,
                "updaterProcessId": 321,
                "updaterStartedAtUtc": "2026-08-27T04:00:00+00:00",
                "createdAtUtc": "2026-08-27T04:00:01+00:00",
                "confirmedAt": None,
            }
        ),
        encoding="utf-8",
    )

    build_next.wait_for_self_update_activation_pointer(
        pointer,
        target=target,
        stage=stage,
        token=token,
        updater_process_id=321,
        timeout_seconds=0.1,
    )

    payload = json.loads(pointer.read_text(encoding="utf-8"))
    payload["unexpected"] = True
    pointer.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(build_next.BuildError, match="pointer fields are invalid"):
        build_next.wait_for_self_update_activation_pointer(
            pointer,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=321,
            timeout_seconds=0.1,
        )

    payload.pop("unexpected")
    for invalid_schema_version in (True, 3):
        payload["schemaVersion"] = invalid_schema_version
        pointer.write_text(json.dumps(payload), encoding="utf-8")
        with pytest.raises(build_next.BuildError, match="pointer fields are invalid"):
            build_next.wait_for_self_update_activation_pointer(
                pointer,
                target=target,
                stage=stage,
                token=token,
                updater_process_id=321,
                timeout_seconds=0.1,
            )


def test_self_update_activation_pointer_accepts_exact_schema_v2_prepared_identity(
    tmp_path: Path,
) -> None:
    target = tmp_path / "VibeTable.Next"
    stage = tmp_path / ".VibeTable.Next.update-smoke"
    pointer = tmp_path / build_next.PENDING_UPDATE_ACTIVATION_POINTER
    token = "b" * 64
    payload = {
        "schemaVersion": 2,
        "state": "prepared",
        "targetRoot": str(target),
        "stagingRoot": str(stage),
        "currentVersion": "1.0.0",
        "targetVersion": "1.0.1",
        "token": token,
        "smokeTest": True,
        "updaterProcessId": 654,
        "updaterStartedAtUtc": "2026-08-27T04:00:00+00:00",
        "createdAtUtc": "2026-08-27T04:00:01+00:00",
        "confirmedAt": None,
        "watchdogProcessId": None,
        "watchdogStartedAtUtc": None,
        "ownedGroupId": None,
        "launchNonce": None,
        "updatedProcessId": None,
        "updatedStartedAtUtc": None,
        "failureCode": None,
        "rollbackRequestedAtUtc": None,
        "ownedGroupQuiescedAtUtc": None,
        "workerLaunchNonce": None,
        "workerProcessId": None,
        "workerStartedAtUtc": None,
        "workerReplacementCount": 0,
        "ownedEntryLedger": [],
        "rollbackAttempt": None,
        "rollbackErrorCode": None,
        "rolledBackAtUtc": None,
    }
    pointer.write_text(json.dumps(payload), encoding="utf-8")

    build_next.wait_for_self_update_activation_pointer(
        pointer,
        target=target,
        stage=stage,
        token=token,
        updater_process_id=654,
        timeout_seconds=0.1,
    )

    invalid_mutations = (
        ("watchdogProcessId", 655, "pointer identity is invalid"),
        ("workerReplacementCount", False, "pointer identity is invalid"),
        ("workerReplacementCount", 0.0, "pointer identity is invalid"),
        ("state", "launchingUpdatedApp", "pointer identity is invalid"),
        ("unexpected", True, "pointer fields are invalid"),
    )
    for field, value, error in invalid_mutations:
        invalid = {**payload, field: value}
        pointer.write_text(json.dumps(invalid), encoding="utf-8")
        with pytest.raises(build_next.BuildError, match=error):
            build_next.wait_for_self_update_activation_pointer(
                pointer,
                target=target,
                stage=stage,
                token=token,
                updater_process_id=654,
                timeout_seconds=0.1,
            )


def _write_strict_self_update_rollback_fixture(
    root: Path,
    *,
    failure_code: str,
    health_failure_readiness: dict[str, object] | None = None,
    consumed_request: Path | None = None,
) -> SimpleNamespace:
    root.mkdir(exist_ok=True)
    target = root / "VibeTable.Next"
    stage = root / ".VibeTable.Next.update-rollback-fixture"
    token = "c" * 64
    updater_process_id = 699
    updated_process_id = 700
    restored_process_id = 701
    if health_failure_readiness is not None:
        readiness_root = root / "self-update-readiness"
        readiness_root.mkdir()
        (readiness_root / "vibetable-readiness.json").write_text(
            json.dumps(health_failure_readiness),
            encoding="utf-8",
        )
    if consumed_request is not None:
        consumed_request.parent.mkdir(parents=True, exist_ok=True)
    restored_readiness_root = root / "self-update-restored-readiness"
    restored_controls_root = root / "self-update-restored-controls"
    restored_readiness_root.mkdir()
    restored_controls_root.mkdir()
    (restored_readiness_root / "vibetable-readiness.json").write_text(
        json.dumps(
            {
                "ready": True,
                "mode": "shell",
                "backendReady": True,
                "webViewReady": True,
                "rendererReady": True,
                "error": None,
                "writtenAt": "2026-08-28T04:00:08+00:00",
            }
        ),
        encoding="utf-8",
    )
    (restored_controls_root / "host-lifecycle-state.json").write_text(
        json.dumps(
            {
                "evidenceKind": "packaged-host-control",
                "action": "visible-startup",
                "hostExecutable": "VibeTable.Next.exe",
                "hostProcessId": restored_process_id,
                "windowVisible": True,
                "trayVisible": False,
                "workspaceId": None,
                "sessionEpoch": None,
                "sessionState": None,
                "error": None,
            }
        ),
        encoding="utf-8",
    )
    receipt = {
        "schemaVersion": 2,
        "state": "rollbackComplete",
        "targetRoot": str(target),
        "stagingRoot": str(stage),
        "currentVersion": "1.0.0",
        "targetVersion": "1.0.1",
        "token": token,
        "smokeTest": True,
        "updaterProcessId": updater_process_id,
        "updaterStartedAtUtc": "2026-08-28T04:00:00+00:00",
        "createdAtUtc": "2026-08-28T04:00:01+00:00",
        "confirmedAt": None,
        "watchdogProcessId": updater_process_id,
        "watchdogStartedAtUtc": "2026-08-28T04:00:00+00:00",
        "ownedGroupId": "d" * 32,
        "launchNonce": None,
        "updatedProcessId": updated_process_id,
        "updatedStartedAtUtc": "2026-08-28T04:00:02+00:00",
        "failureCode": failure_code,
        "rollbackRequestedAtUtc": "2026-08-28T04:00:04+00:00",
        "ownedGroupQuiescedAtUtc": "2026-08-28T04:00:05+00:00",
        "workerLaunchNonce": None,
        "workerProcessId": 697,
        "workerStartedAtUtc": "2026-08-28T04:00:06+00:00",
        "workerReplacementCount": 0,
        "ownedEntryLedger": [
            {"name": "resources", "phase": "restored"},
            {"name": "release.json", "phase": "restored"},
            {"name": "VibeTable.Next.exe", "phase": "restored"},
        ],
        "rollbackAttempt": "f" * 32,
        "rollbackErrorCode": None,
        "rolledBackAtUtc": "2026-08-28T04:00:07+00:00",
    }
    receipt_path = root / f".VibeTable.Next.update-rollback-{'f' * 32}.json"
    receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            snapshot=lambda: SimpleNamespace(
                members=(
                    SimpleNamespace(
                        pid=restored_process_id,
                        executable_name="VibeTable.Next.exe",
                        identity_verified=True,
                    ),
                )
            )
        ),
    )
    return SimpleNamespace(
        root=root,
        target=target,
        stage=stage,
        token=token,
        updater_process_id=updater_process_id,
        updated_process_id=updated_process_id,
        restored_process_id=restored_process_id,
        receipt=receipt,
        receipt_path=receipt_path,
        process_scope=process_scope,
        consumed_request=consumed_request,
    )


def test_strict_self_update_rollback_fixture_builder_supports_all_scenarios(
    tmp_path: Path,
) -> None:
    health = _write_strict_self_update_rollback_fixture(
        tmp_path / "health",
        failure_code="workspaceHealthProbeFailed",
        health_failure_readiness={"ready": False, "error": "health failed"},
    )
    updated_request = tmp_path / "updated" / "updated-controls" / "close.request"
    updated = _write_strict_self_update_rollback_fixture(
        tmp_path / "updated",
        failure_code="updatedProcessExited",
        consumed_request=updated_request,
    )
    timeout_request = tmp_path / "timeout" / "updated-controls" / "hold.request"
    timeout = _write_strict_self_update_rollback_fixture(
        tmp_path / "timeout",
        failure_code="healthTimeout",
        consumed_request=timeout_request,
    )

    assert set(health.receipt) == {
        "schemaVersion",
        "state",
        "targetRoot",
        "stagingRoot",
        "currentVersion",
        "targetVersion",
        "token",
        "smokeTest",
        "updaterProcessId",
        "updaterStartedAtUtc",
        "createdAtUtc",
        "confirmedAt",
        "watchdogProcessId",
        "watchdogStartedAtUtc",
        "ownedGroupId",
        "launchNonce",
        "updatedProcessId",
        "updatedStartedAtUtc",
        "failureCode",
        "rollbackRequestedAtUtc",
        "ownedGroupQuiescedAtUtc",
        "workerLaunchNonce",
        "workerProcessId",
        "workerStartedAtUtc",
        "workerReplacementCount",
        "ownedEntryLedger",
        "rollbackAttempt",
        "rollbackErrorCode",
        "rolledBackAtUtc",
    }
    assert health.receipt["failureCode"] == "workspaceHealthProbeFailed"
    assert updated.receipt["failureCode"] == "updatedProcessExited"
    assert updated.consumed_request == updated_request
    assert timeout.receipt["failureCode"] == "healthTimeout"
    assert timeout.consumed_request == timeout_request


def test_self_update_health_failure_requires_exact_rollback_and_restored_readiness(
    tmp_path: Path,
) -> None:
    fixture = _write_strict_self_update_rollback_fixture(
        tmp_path,
        failure_code="workspaceHealthProbeFailed",
        health_failure_readiness={
            "ready": False,
            "mode": None,
            "error": "Post-update workspace health probe failed",
            "writtenAt": "2026-08-28T04:00:03+00:00",
        },
    )
    target = fixture.target
    stage = fixture.stage
    token = fixture.token
    updater_process_id = fixture.updater_process_id
    updated_process_id = fixture.updated_process_id
    restored_process_id = fixture.restored_process_id
    receipt = fixture.receipt
    process_scope = fixture.process_scope

    assert (
        build_next.wait_for_self_update_health_failure_rollback(
            tmp_path,
            process_scope=process_scope,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=updater_process_id,
            updated_process_id=updated_process_id,
            timeout_seconds=0.1,
        )
        == restored_process_id
    )

    invalid_members = (
        SimpleNamespace(
            pid=restored_process_id,
            executable_name=build_next.HOST_EXE_NAME,
            identity_verified=False,
        ),
        SimpleNamespace(
            pid=restored_process_id,
            executable_name="wrong-host.exe",
            identity_verified=True,
        ),
    )
    for invalid_member in invalid_members:
        invalid_scope = cast(
            WindowsProcessScope,
            SimpleNamespace(
                snapshot=lambda member=invalid_member: SimpleNamespace(members=(member,))
            ),
        )
        with pytest.raises(
            build_next.BuildError,
            match="health-failure rollback did not complete",
        ):
            build_next.wait_for_self_update_health_failure_rollback(
                tmp_path,
                process_scope=invalid_scope,
                target=target,
                stage=stage,
                token=token,
                updater_process_id=updater_process_id,
                updated_process_id=updated_process_id,
                timeout_seconds=0.01,
            )

    invalid_receipts = (
        {**receipt, "schemaVersion": 2.0},
        {**receipt, "updaterProcessId": True},
        {**receipt, "updaterStartedAtUtc": "2026-08-28T04:00:00"},
        {**receipt, "createdAtUtc": "2026-08-28T03:59:59+00:00"},
        {**receipt, "updaterProcessId": 698, "watchdogProcessId": 698},
        {**receipt, "watchdogProcessId": 698},
        {**receipt, "watchdogStartedAtUtc": "2026-08-28T04:00:01+00:00"},
        {**receipt, "ownedGroupId": "not-lower-hex"},
        {**receipt, "launchNonce": "a" * 64},
        {**receipt, "updatedProcessId": float(updated_process_id)},
        {**receipt, "updatedStartedAtUtc": "2026-08-28T04:00:00+00:00"},
        {**receipt, "failureCode": "updatedProcessExited"},
        {**receipt, "rollbackRequestedAtUtc": 0},
        {**receipt, "ownedGroupQuiescedAtUtc": None},
        {**receipt, "workerLaunchNonce": "b" * 64},
        {**receipt, "workerProcessId": 0},
        {**receipt, "workerStartedAtUtc": "2026-08-28T04:00:04+00:00"},
        {**receipt, "workerReplacementCount": True},
        {**receipt, "workerReplacementCount": 2},
        {
            **receipt,
            "ownedEntryLedger": [
                {"name": "resources", "phase": "restored", "unexpected": True},
                *receipt["ownedEntryLedger"][1:],
            ],
        },
        {
            **receipt,
            "ownedEntryLedger": [
                {"name": ["resources"], "phase": "restored"},
                *receipt["ownedEntryLedger"][1:],
            ],
        },
        {**receipt, "rolledBackAtUtc": "2026-08-28T04:00:05+00:00"},
    )
    receipt_path = fixture.receipt_path
    for invalid_receipt in invalid_receipts:
        receipt_path.write_text(json.dumps(invalid_receipt), encoding="utf-8")
        with pytest.raises(
            build_next.BuildError,
            match="rollback receipt identity is invalid",
        ):
            build_next.wait_for_self_update_health_failure_rollback(
                tmp_path,
                process_scope=process_scope,
                target=target,
                stage=stage,
                token=token,
                updater_process_id=updater_process_id,
                updated_process_id=updated_process_id,
                timeout_seconds=0.01,
            )


def test_self_update_updated_exit_accepts_strict_rollback_without_health_failure_readiness(
    tmp_path: Path,
) -> None:
    close_request = tmp_path / "self-update-updated-controls" / "host-normal-close.request"
    fixture = _write_strict_self_update_rollback_fixture(
        tmp_path,
        failure_code="updatedProcessExited",
        consumed_request=close_request,
    )
    target = fixture.target
    stage = fixture.stage
    token = fixture.token
    updater_process_id = fixture.updater_process_id
    updated_process_id = fixture.updated_process_id
    restored_process_id = fixture.restored_process_id
    process_scope = fixture.process_scope
    assert (
        build_next.wait_for_self_update_updated_exit_rollback(
            tmp_path,
            process_scope=process_scope,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=updater_process_id,
            updated_process_id=updated_process_id,
            consumed_request=close_request,
            timeout_seconds=0.1,
        )
        == restored_process_id
    )

    fixture.receipt_path.write_text(
        json.dumps({**fixture.receipt, "failureCode": "workspaceHealthProbeFailed"}),
        encoding="utf-8",
    )
    with pytest.raises(build_next.BuildError, match="rollback receipt identity is invalid"):
        build_next.wait_for_self_update_updated_exit_rollback(
            tmp_path,
            process_scope=process_scope,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=updater_process_id,
            updated_process_id=updated_process_id,
            consumed_request=close_request,
            timeout_seconds=0.1,
        )

    fixture.receipt_path.write_text(json.dumps(fixture.receipt), encoding="utf-8")
    close_request.write_text("", encoding="utf-8")
    with pytest.raises(build_next.BuildError, match="did not consume updated close request"):
        build_next.wait_for_self_update_updated_exit_rollback(
            tmp_path,
            process_scope=process_scope,
            target=target,
            stage=stage,
            token=token,
            updater_process_id=updater_process_id,
            updated_process_id=updated_process_id,
            consumed_request=close_request,
            timeout_seconds=0.1,
        )


def test_self_update_health_timeout_accepts_strict_rollback_after_hold_consumption(
    tmp_path: Path,
) -> None:
    scenario = build_next._HEALTH_TIMEOUT_ROLLBACK_SCENARIO
    evidence = scenario.arrange_failure(tmp_path)
    assert evidence.consumed_request is not None
    hold_request = evidence.consumed_request.path
    fixture = _write_strict_self_update_rollback_fixture(
        tmp_path,
        failure_code="healthTimeout",
        consumed_request=hold_request,
    )

    def wait_for_rollback() -> int:
        return build_next._wait_for_self_update_rollback(
            tmp_path,
            process_scope=fixture.process_scope,
            target=fixture.target,
            stage=fixture.stage,
            token=fixture.token,
            updater_process_id=fixture.updater_process_id,
            updated_process_id=fixture.updated_process_id,
            scenario_slug=scenario.slug,
            expected_failure_code=scenario.failure_code,
            health_failure_readiness=evidence.health_failure_readiness,
            consumed_request=evidence.consumed_request,
            timeout_seconds=0.1,
        )

    hold_request.unlink()
    assert wait_for_rollback() == fixture.restored_process_id

    fixture.receipt_path.write_text(
        json.dumps({**fixture.receipt, "failureCode": "updatedProcessExited"}),
        encoding="utf-8",
    )
    with pytest.raises(build_next.BuildError, match="rollback receipt identity is invalid"):
        wait_for_rollback()

    fixture.receipt_path.write_text(json.dumps(fixture.receipt), encoding="utf-8")
    hold_request.write_text("", encoding="utf-8")
    with pytest.raises(build_next.BuildError, match="did not consume health-timeout hold request"):
        wait_for_rollback()


def test_restored_self_update_host_close_rejects_process_that_already_exited(
    tmp_path: Path,
) -> None:
    controls = tmp_path / "controls"
    controls.mkdir()
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(snapshot=lambda: SimpleNamespace(members=())),
    )

    with pytest.raises(
        build_next.BuildError,
        match="restored process exited before close request",
    ):
        build_next.request_restored_self_update_host_close(controls, 701, process_scope)

    assert not (controls / "host-normal-close.request").exists()


def test_restored_self_update_host_close_reports_remaining_job_members(
    tmp_path: Path,
) -> None:
    controls = tmp_path / "controls"
    controls.mkdir()
    initial_members = (SimpleNamespace(pid=701),)
    remaining_members = (
        SimpleNamespace(
            pid=701,
            executable_name="VibeTable.Next.exe",
            identity_verified=True,
        ),
        SimpleNamespace(
            pid=702,
            executable_name="msedgewebview2.exe",
            identity_verified=False,
        ),
    )
    snapshots = iter(
        (
            SimpleNamespace(members=initial_members),
            SimpleNamespace(members=remaining_members),
        )
    )
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            snapshot=lambda: next(snapshots),
            wait_empty=lambda *, timeout: SimpleNamespace(
                success=False,
                remaining_pids=(701, 702),
                errors=("Job still contains processes after the wait deadline",),
            ),
        ),
    )

    with pytest.raises(build_next.BuildError) as captured:
        build_next.request_restored_self_update_host_close(controls, 701, process_scope)

    message = str(captured.value)
    assert "restoredProcessId=701" in message
    assert "restoredProcessInJob=true" in message
    assert "pid=701, executable=VibeTable.Next.exe, identityVerified=true" in message
    assert "pid=702, executable=msedgewebview2.exe, identityVerified=false" in message
    assert "Job still contains processes after the wait deadline" in message


@pytest.mark.parametrize(
    "snapshot_error",
    [
        ProcessScopeQueryError("sensitive snapshot detail"),
        OSError("sensitive snapshot detail"),
    ],
)
def test_restored_self_update_host_close_preserves_wait_diagnostics_when_snapshot_fails(
    tmp_path: Path,
    snapshot_error: Exception,
) -> None:
    controls = tmp_path / "controls"
    controls.mkdir()
    snapshot_calls = 0

    def snapshot() -> SimpleNamespace:
        nonlocal snapshot_calls
        snapshot_calls += 1
        if snapshot_calls == 1:
            return SimpleNamespace(members=(SimpleNamespace(pid=701),))
        raise snapshot_error

    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            snapshot=snapshot,
            wait_empty=lambda *, timeout: SimpleNamespace(
                success=False,
                remaining_pids=(701, 702),
                errors=("Job wait timed out",),
            ),
        ),
    )

    with pytest.raises(build_next.BuildError) as captured:
        build_next.request_restored_self_update_host_close(controls, 701, process_scope)

    message = str(captured.value)
    assert "restored process did not exit" in message
    assert "waitRemainingPids=[701, 702]" in message
    assert "waitErrors=[Job wait timed out]" in message
    assert "restoredProcessInJob=true" in message
    assert "snapshotUnavailable=true" in message
    assert f"snapshotError={type(snapshot_error).__name__}" in message
    assert "sensitive snapshot detail" not in message


def test_restored_self_update_host_close_bounds_failure_diagnostics(tmp_path: Path) -> None:
    controls = tmp_path / "controls"
    controls.mkdir()
    remaining_members = tuple(
        SimpleNamespace(
            pid=1000 + index,
            executable_name=f"member-{index}.exe",
            identity_verified=index % 2 == 0,
        )
        for index in range(12)
    )
    snapshots = iter(
        (
            SimpleNamespace(members=(SimpleNamespace(pid=1000),)),
            SimpleNamespace(members=remaining_members),
        )
    )
    errors = tuple(f"error-{index}-" + ("x" * 300) + "-sentinel-tail" for index in range(6))
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            snapshot=lambda: next(snapshots),
            wait_empty=lambda *, timeout: SimpleNamespace(
                success=False,
                remaining_pids=tuple(range(1000, 1012)),
                errors=errors,
            ),
        ),
    )

    with pytest.raises(build_next.BuildError) as captured:
        build_next.request_restored_self_update_host_close(controls, 1000, process_scope)

    message = str(captured.value)
    assert (
        "waitRemainingPids=[1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007; omitted=4]" in message
    )
    assert "pid=1007, executable=member-7.exe" in message
    assert "pid=1008" not in message
    assert "error-0-" in message
    assert "error-3-" in message
    assert "error-4-" not in message
    assert "sentinel-tail" not in message
    assert "waitErrorsOmitted=2" in message
    assert len(message) < 2000


def test_restored_self_update_host_close_requires_control_consumption(tmp_path: Path) -> None:
    controls = tmp_path / "controls"
    controls.mkdir()
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            snapshot=lambda: SimpleNamespace(members=(SimpleNamespace(pid=701),)),
            wait_empty=lambda *, timeout: SimpleNamespace(success=True),
        ),
    )

    with pytest.raises(
        build_next.BuildError,
        match="restored process did not consume close request",
    ):
        build_next.request_restored_self_update_host_close(controls, 701, process_scope)

    assert (controls / "host-normal-close.request").is_file()


def test_self_update_cleanup_terminates_the_owned_scope_only(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, float]] = []
    process_scope = cast(
        WindowsProcessScope,
        SimpleNamespace(
            wait_empty=lambda *, timeout: (
                calls.append(("wait", timeout)) or SimpleNamespace(success=False)
            ),
            terminate_all=lambda *, timeout: (
                calls.append(("terminate", timeout)) or SimpleNamespace(success=True)
            ),
        ),
    )
    monkeypatch.setattr(
        build_next,
        "terminate_windows_process_tree",
        lambda process_id: pytest.fail(f"unexpected bare-PID termination: {process_id}"),
    )

    build_next.cleanup_self_update_process_scope(process_scope)

    assert calls == [("wait", 0), ("terminate", 30)]


def test_self_update_rollback_batch_delegates_scenarios_in_release_order(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, str, str]] = []

    def run_shared(
        _package: Path,
        root: Path,
        *,
        scenario: SimpleNamespace,
    ) -> None:
        calls.append(
            (
                root.name,
                str(scenario.slug),
                str(scenario.failure_code),
            )
        )

    monkeypatch.setattr(build_next, "_run_desktop_self_update_rollback_smoke", run_shared)

    package = tmp_path / "package"
    build_next._run_desktop_self_update_rollback_smokes(package, tmp_path)

    assert calls == [
        ("health-failure", "health-failure", "workspaceHealthProbeFailed"),
        ("updated-exit", "updated-exit", "updatedProcessExited"),
        ("health-timeout", "health-timeout", "healthTimeout"),
    ]


def test_self_update_rollback_scenarios_bound_updater_wait_to_failure_budget() -> None:
    assert [
        scenario.updater_wait_timeout_seconds
        for scenario in build_next._SELF_UPDATE_ROLLBACK_SCENARIOS
    ] == [120, 120, 180]


def test_health_failure_blocker_is_owned_before_plan_preparation(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, float | None]] = []

    class FakeBlocker:
        pid = 123

        @staticmethod
        def poll() -> None:
            return None

        @staticmethod
        def terminate() -> None:
            calls.append(("terminate", None))

        @staticmethod
        def wait(timeout: float | None = None) -> int:
            calls.append(("wait", timeout))
            return 0

    def stage(destination: Path, *_args: object, **_kwargs: object) -> None:
        destination.mkdir(parents=True)

    monkeypatch.setattr(build_next, "_stage_self_update_smoke_package", stage)
    monkeypatch.setattr(build_next.subprocess, "Popen", lambda *_args, **_kwargs: FakeBlocker())
    monkeypatch.setattr(
        build_next.secrets,
        "token_hex",
        lambda _size: (_ for _ in ()).throw(RuntimeError("plan preparation failed")),
    )

    with pytest.raises(RuntimeError, match="plan preparation failed"):
        build_next._run_desktop_self_update_rollback_smoke(
            tmp_path / "package",
            tmp_path / "health-failure",
            scenario=build_next._HEALTH_FAILURE_ROLLBACK_SCENARIO,
        )

    assert calls == [("terminate", None), ("wait", 30)]


def test_health_failure_user_data_sentinel_uses_packaged_host_local_data_root(
    tmp_path: Path,
) -> None:
    local_data = build_next.self_update_smoke_local_data_root(tmp_path)

    assert local_data == (tmp_path / build_next.SELF_UPDATE_SMOKE_READINESS_DIR / "local-data")
    assert local_data / "user-data.db" != (
        local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
    )


def test_self_update_activation_rejects_cleanup_before_shell_readiness(
    tmp_path: Path,
) -> None:
    completion = tmp_path / build_next.SELF_UPDATE_SMOKE_COMPLETION_FILE
    readiness = (
        tmp_path / build_next.SELF_UPDATE_SMOKE_READINESS_DIR / build_next.SHELL_READINESS_FILE
    )
    completion.write_text("token", encoding="ascii")

    with pytest.raises(
        build_next.BuildError,
        match="cleaned staging before shell readiness",
    ):
        build_next.wait_for_self_update_activation(
            completion,
            readiness,
            "token",
            "1.0.1",
            123,
            timeout_seconds=0.1,
        )


def test_self_update_activation_requires_all_shell_boundaries(tmp_path: Path) -> None:
    completion = tmp_path / build_next.SELF_UPDATE_SMOKE_COMPLETION_FILE
    readiness = (
        tmp_path / build_next.SELF_UPDATE_SMOKE_READINESS_DIR / build_next.SHELL_READINESS_FILE
    )
    readiness.parent.mkdir()
    readiness.write_text(
        json.dumps(
            {
                "ready": True,
                "mode": "shell",
                "backendReady": True,
                "webViewReady": True,
                "rendererReady": False,
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(build_next.BuildError, match="readiness is incomplete"):
        build_next.wait_for_self_update_activation(
            completion,
            readiness,
            "token",
            "1.0.1",
            123,
            timeout_seconds=0.1,
        )


def test_self_update_activation_requires_workspace_probe_evidence(tmp_path: Path) -> None:
    completion = tmp_path / build_next.SELF_UPDATE_SMOKE_COMPLETION_FILE
    readiness = (
        tmp_path / build_next.SELF_UPDATE_SMOKE_READINESS_DIR / build_next.SHELL_READINESS_FILE
    )
    readiness.parent.mkdir()
    readiness.write_text(
        json.dumps(
            {
                "ready": True,
                "mode": "shell",
                "backendReady": True,
                "webViewReady": True,
                "rendererReady": True,
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(build_next.BuildError, match="workspace probe evidence is invalid"):
        build_next.wait_for_self_update_activation(
            completion,
            readiness,
            "token",
            "1.0.1",
            123,
            timeout_seconds=0.1,
        )


def test_self_update_activation_binds_readiness_before_completion(
    tmp_path: Path,
) -> None:
    completion = tmp_path / build_next.SELF_UPDATE_SMOKE_COMPLETION_FILE
    readiness = (
        tmp_path / build_next.SELF_UPDATE_SMOKE_READINESS_DIR / build_next.SHELL_READINESS_FILE
    )
    readiness.parent.mkdir()
    readiness.write_text(
        json.dumps(
            {
                "ready": True,
                "mode": "shell",
                "backendReady": True,
                "webViewReady": True,
                "rendererReady": True,
                "workspaceProbe": {
                    "status": "skippedNoRegisteredWorkspace",
                    "workspaceId": None,
                    "sessionEpoch": None,
                    "tableCount": None,
                },
                "writtenAt": "2026-08-27T03:00:00+00:00",
            }
        ),
        encoding="utf-8",
    )
    completion.write_text(
        json.dumps(
            {
                "token": "token",
                "targetVersion": "1.0.1",
                "processId": os.getpid(),
                "confirmedAt": "2026-08-27T03:00:01+00:00",
            }
        ),
        encoding="utf-8",
    )

    payload = build_next.wait_for_self_update_activation(
        completion,
        readiness,
        "token",
        "1.0.1",
        os.getpid(),
        timeout_seconds=0.1,
    )

    assert payload["ready"] is True


def test_windows_process_open_failure_only_accepts_a_missing_pid() -> None:
    assert build_next.windows_process_exited_after_open_failure(87) is True

    with pytest.raises(build_next.BuildError, match="Win32 error 5"):
        build_next.windows_process_exited_after_open_failure(5)


def test_release_archive_name_contains_version_platform_and_architecture() -> None:
    assert build_next.release_archive_name("1.2.3") == "VibeTable-v1.2.3-win-x64.zip"


def test_sidecar_build_is_trimmed_reproducible_and_version_stamped() -> None:
    paths = build_next.RepoPaths.default(REPO_ROOT)
    command = build_next.build_sidecar_command(
        paths,
        output=paths.staging_root / "sidecar" / "vibetable-pb.exe",
        commit="abc123",
        build_time="2026-07-24T00:00:00Z",
    )

    assert command[:3] == [build_next.resolve_go(paths.repo_root), "build", "-trimpath"]
    assert "-buildvcs=true" in command
    assert "-ldflags" in command
    ldflags = command[command.index("-ldflags") + 1]
    assert f"buildinfo.Version={read_project_version(REPO_ROOT)}" in ldflags
    assert "buildinfo.Commit=abc123" in ldflags
    assert command[-1] == "./cmd/vibetable-pb"


def test_stage_release_assets_records_binary_hash_build_info_and_sbom(
    tmp_path: Path,
) -> None:
    defaults = build_next.RepoPaths.default(REPO_ROOT)
    paths = defaults.with_output_roots(
        staging_root=tmp_path / "staging",
        scratch_root=tmp_path / "scratch",
        publish_root=tmp_path / "publish",
    ).staging_mirror()
    binary = paths.sidecar_binary
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"fixed-sidecar")
    _write_recovery_tools(paths)
    build_info = _release_build_info()
    license_dir = tmp_path / "pocketbase-module"
    license_dir.mkdir()
    (license_dir / "LICENSE").write_text(
        "Mozilla Public License Version 2.0\n",
        encoding="utf-8",
    )

    build_next.stage_sidecar_assets(
        paths,
        build_info=build_info,
        modules=_release_modules(
            license_dir,
            ("github.com/pocketbase/pocketbase", "v0.40.1"),
        ),
    )

    digest = hashlib.sha256(b"fixed-sidecar").hexdigest()
    assert paths.sidecar_checksum.read_text(encoding="utf-8").strip() == digest
    assert json.loads(paths.sidecar_build_info.read_text(encoding="utf-8")) == build_info
    sbom = json.loads(paths.sidecar_sbom.read_text(encoding="utf-8"))
    assert sbom["bomFormat"] == "CycloneDX"
    assert sbom["components"][0]["name"] == "github.com/pocketbase/pocketbase"
    assert paths.sidecar_licenses.is_file()
    assert "UNKNOWN" not in paths.sidecar_licenses.read_text(encoding="utf-8")
    assert (paths.sidecar_assets_dir / "migrations" / "manifest.json").is_file()


def test_package_verifier_rejects_tampered_sidecar(tmp_path: Path) -> None:
    defaults = build_next.RepoPaths.default(REPO_ROOT)
    stage = defaults.with_output_roots(
        staging_root=tmp_path / "staging",
        scratch_root=tmp_path / "scratch",
        publish_root=tmp_path / "publish",
    ).staging_mirror()
    stage.sidecar_binary.parent.mkdir(parents=True)
    stage.sidecar_binary.write_bytes(b"sidecar")
    _write_recovery_tools(stage)
    license_dir = tmp_path / "dependency-module"
    license_dir.mkdir()
    (license_dir / "LICENSE").write_text(
        "Mozilla Public License Version 2.0\n",
        encoding="utf-8",
    )
    build_next.stage_sidecar_assets(
        stage,
        build_info=_release_build_info(),
        modules=_release_modules(
            license_dir,
            ("example.invalid/dependency", "v1.0.0"),
        ),
    )
    build_next.write_manifest(stage)
    stage.sidecar_binary.write_bytes(b"tampered")

    with pytest.raises(build_next.BuildError, match="SHA-256"):
        build_next.verify_sidecar_package(stage)


def test_write_source_manifest_uses_zero_binary_digest(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    paths = build_next.RepoPaths.default(tmp_path)
    (tmp_path / "desktop").mkdir()
    observed: list[str | None] = []

    def render(_paths: build_next.RepoPaths, *, sidecar_sha256: str | None = None) -> str:
        observed.append(sidecar_sha256)
        return '{"generated":true}\n'

    monkeypatch.setattr(build_next, "render_manifest", render)

    target = build_next.write_source_manifest(paths)

    assert target == tmp_path / "desktop" / "publish-layout.json"
    assert target.read_text(encoding="utf-8") == '{"generated":true}\n'
    assert observed == ["0" * 64]


def test_package_contract_validates_v2_formats_recovery_and_bundled_tools(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    defaults = build_next.RepoPaths.default(REPO_ROOT)
    stage = defaults.with_output_roots(
        staging_root=tmp_path / "staging",
        scratch_root=tmp_path / "scratch",
        publish_root=tmp_path / "publish",
    ).staging_mirror()
    stage.host_exe.parent.mkdir(parents=True)
    stage.host_exe.write_bytes(b"host")
    backend = stage.backend_dir / build_next.BACKEND_EXE_NAME
    backend.parent.mkdir(parents=True)
    backend.write_bytes(b"backend")
    stage.web_grid_publish_dir.mkdir()
    (stage.web_grid_publish_dir / "index.html").write_text(
        "<!doctype html>",
        encoding="utf-8",
    )
    stage.sidecar_binary.parent.mkdir()
    stage.sidecar_binary.write_bytes(b"sidecar")
    _write_recovery_tools(stage)
    license_dir = tmp_path / "dependency-module"
    license_dir.mkdir()
    (license_dir / "LICENSE").write_text(
        "Mozilla Public License Version 2.0\n",
        encoding="utf-8",
    )
    modules = [
        {
            "path": name,
            "version": version,
            "license": "MPL-2.0",
            "dir": str(license_dir),
        }
        for name, version in (
            ("github.com/pocketbase/pocketbase", "v0.40.1"),
            ("github.com/google/cel-go", "v0.31.0"),
            ("github.com/kopia/kopia", build_next.KOPIA_VERSION),
            ("filippo.io/age", build_next.AGE_VERSION),
        )
    ]
    build_next.stage_sidecar_assets(
        stage,
        build_info=_release_build_info(),
        modules=modules,
    )
    desktop_contracts = stage.resources_dir / "contracts" / "v2"
    desktop_contracts.mkdir(parents=True)
    (desktop_contracts / "provider-support.json").write_text(
        "{}",
        encoding="utf-8",
    )
    build_next.stage_workspace_contracts(stage)
    assert (desktop_contracts / "provider-support.json").read_bytes() == (
        REPO_ROOT / "contracts" / "v2" / "provider-support.json"
    ).read_bytes()
    shutil.copy2(
        REPO_ROOT / "docs" / "RECOVERY.md",
        stage.resources_dir / "RECOVERY.md",
    )
    build_next.write_manifest(stage)
    build_next.write_release_manifest(stage)
    lock = build_next.load_recovery_tool_lock(REPO_ROOT)

    def go_metadata(_go: str, path: Path) -> dict[str, object]:
        if path.name == build_next.KOPIA_EXE_NAME:
            package = module = "github.com/kopia/kopia"
            version, module_sum = lock.kopia_version, lock.kopia_sum
        else:
            package = (
                "filippo.io/age/cmd/age-keygen"
                if path.name == build_next.AGE_KEYGEN_EXE_NAME
                else "filippo.io/age/cmd/age"
            )
            module = "filippo.io/age"
            version, module_sum = lock.age_version, lock.age_sum
        return {
            "package": package,
            "module": module,
            "version": version,
            "moduleSum": module_sum,
            "goVersion": f"go{lock.go_version}",
            "build": {"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "0"},
        }

    monkeypatch.setattr(package_check, "go_binary_metadata", go_metadata)

    assert not any(
        path.name == "__pycache__" or path.suffix == ".pyc"
        for path in (stage.resources_dir / "contracts" / "v2").rglob("*")
    )
    assert check_package(stage.publish_root) == []

    layout = json.loads(stage.manifest_path.read_text(encoding="utf-8"))
    layout["formats"]["package"] = 1
    stage.manifest_path.write_text(
        json.dumps(layout),
        encoding="utf-8",
    )
    cache = stage.resources_dir / "contracts" / "v2" / "__pycache__"
    cache.mkdir()
    (cache / "generator.pyc").write_bytes(b"cache")
    errors = check_package(stage.publish_root)
    assert "package format versions do not match workspace v2" in errors
    assert any("packaged build cache is forbidden" in error for error in errors)

    sbom = json.loads(stage.sidecar_sbom.read_text(encoding="utf-8"))
    next(item for item in sbom["components"] if item["name"] == "github.com/kopia/kopia")[
        "version"
    ] = "v0.0.0"
    stage.sidecar_sbom.write_text(json.dumps(sbom), encoding="utf-8")
    assert any(
        "SBOM dependency version mismatch: github.com/kopia/kopia" in error
        for error in check_package(stage.publish_root)
    )

    (desktop_contracts / "workspace-version-policy.json").unlink()
    assert "missing workspace contract asset: workspace-version-policy.json" in check_package(
        stage.publish_root
    )

    policy_path = desktop_contracts / "workspace-version-policy.json"
    shutil.copy2(REPO_ROOT / "contracts/v2/workspace-version-policy.json", policy_path)
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["fabricatedCompatibilityClaim"] = "verified"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")
    assert "packaged workspace version policy violates its closed schema" in check_package(
        stage.publish_root
    )

    shutil.copy2(REPO_ROOT / "contracts/v2/workspace-version-policy.json", policy_path)
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["writerCompatibility"]["accepted"][0]["appVersion"] = "0.4.0"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")
    assert any(
        "accepted current writer must match currentWriter.appVersion" in error
        for error in check_package(stage.publish_root)
    )

    shutil.copy2(REPO_ROOT / "contracts/v2/workspace-version-policy.json", policy_path)
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["compatibilityCorpus"]["immutablePrefix"]["formalReleaseCount"] = 1
    policy_path.write_text(json.dumps(policy), encoding="utf-8")
    corpus_path = desktop_contracts / "compatibility-corpus.json"
    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
    corpus["previousFormalReleases"] = [
        {
            "writerVersion": "0.5.0",
            "sourceRelease": {
                "tag": "v0.5.0",
                "sourceCommit": "a" * 40,
                "assetName": "VibeTable-v0.5.0-win-x64.zip",
            },
            "artifacts": [
                {
                    "id": "workspace",
                    "kind": "workspace-archive",
                    "path": "fixtures/missing-formal-workspace.zip",
                    "sha256": "a" * 64,
                },
                {
                    "id": "snapshot",
                    "kind": "snapshot-package",
                    "path": "fixtures/missing-formal-snapshot.vtsnapshot",
                    "sha256": "b" * 64,
                },
                {
                    "id": "invalid",
                    "kind": "workspace-archive",
                    "path": "fixtures/missing-formal-invalid.zip",
                    "sha256": "c" * 64,
                },
            ],
            "cases": [
                {
                    "operation": "workspace.open",
                    "artifactId": "workspace",
                    "expected": "read",
                },
                {
                    "operation": "snapshot.import",
                    "artifactId": "snapshot",
                    "expected": "migrate",
                },
                {
                    "operation": "workspace.open",
                    "artifactId": "invalid",
                    "expected": "reject-zero-write",
                },
            ],
        }
    ]
    corpus_path.write_text(json.dumps(corpus), encoding="utf-8")
    assert any(
        "formal release corpus evidence prefix is invalid" in error
        for error in check_package(stage.publish_root)
    )

    shutil.copy2(REPO_ROOT / "contracts/v2/workspace-version-policy.json", policy_path)
    shutil.copy2(REPO_ROOT / "contracts/v2/compatibility-corpus.json", corpus_path)
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    policy["currentWriter"]["appVersion"] = "9.9.9"
    policy["writerCompatibility"]["accepted"][0]["appVersion"] = "9.9.9"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")
    errors = check_package(stage.publish_root)
    assert "packaged workspace version policy does not match source" in errors
    assert "packaged workspace version policy does not match release identity" in errors

    shutil.copy2(REPO_ROOT / "contracts/v2/workspace-version-policy.json", policy_path)
    restored_policy = json.loads(policy_path.read_text(encoding="utf-8"))
    assert restored_policy["compatibilityCorpus"]["immutablePrefix"]["formalReleaseCount"] == 0
    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
    baseline_artifact = corpus["baselines"][0]["artifacts"][0]
    baseline_path = desktop_contracts / baseline_artifact["path"]
    baseline_path.write_bytes(b"fabricated packaged baseline")
    baseline_artifact["sha256"] = hashlib.sha256(baseline_path.read_bytes()).hexdigest()
    corpus_path.write_text(json.dumps(corpus), encoding="utf-8")
    errors = check_package(stage.publish_root)
    assert "packaged workspace compatibility corpus does not match source" in errors
    assert not any(
        "workspace compatibility corpus artifact is missing or changed" in error for error in errors
    )


def test_recovery_tool_versions_and_sums_have_one_committed_lock() -> None:
    lock = build_next.load_recovery_tool_lock(REPO_ROOT)
    assert lock.go_version == build_next.RECOVERY_GO_VERSION
    assert lock.kopia_version == build_next.KOPIA_VERSION
    assert lock.age_version == build_next.AGE_VERSION
    assert lock.kopia_sum.startswith("h1:")
    assert lock.age_sum.startswith("h1:")
    dependency_manifest = json.loads(
        (REPO_ROOT / "tools" / "workspace-storage-dependencies.json").read_text(encoding="utf-8")
    )
    assert dependency_manifest["recoveryToolLock"] == "tools/recovery-tools/go.mod"
    assert "version" not in dependency_manifest["dependencies"]["kopia"]
    assert "version" not in dependency_manifest["dependencies"]["age"]


def test_release_build_prefers_the_exact_versioned_go_toolchain(
    tmp_path: Path,
) -> None:
    suffix = "go.exe" if os.name == "nt" else "go"
    exact = tmp_path / ".tools" / f"go-{build_next.RECOVERY_GO_VERSION}" / "go" / "bin" / suffix
    stale = tmp_path / ".tools" / "go-full" / "go" / "bin" / suffix
    exact.parent.mkdir(parents=True)
    stale.parent.mkdir(parents=True)
    exact.write_bytes(b"exact")
    stale.write_bytes(b"stale")

    assert build_next.resolve_go(tmp_path) == str(exact)


def test_release_candidate_report_binds_the_exact_package_tree_and_archive(
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "VibeTable.Next"
    (package_root / "resources" / "sidecar").mkdir(parents=True)
    (package_root / "VibeTable.Next.exe").write_bytes(b"host")
    (package_root / "resources" / "sidecar" / "vibetable-pb.exe").write_bytes(b"sidecar")
    (package_root / "release.json").write_text(
        json.dumps(
            {
                "product": "VibeTable",
                "version": "1.2.3",
                "platform": "windows",
                "architecture": "x64",
            }
        ),
        encoding="utf-8",
    )
    archive = tmp_path / "VibeTable-v1.2.3-win-x64.zip"

    evidence = release_candidate.create_archive(package_root, archive)
    assert evidence["archive"]["rootDirectory"] == "VibeTable"
    with zipfile.ZipFile(archive) as created:
        assert created.namelist()
        assert all(name.startswith("VibeTable/") for name in created.namelist())
    report = tmp_path / "release-eligibility.json"
    report.write_text(
        json.dumps(
            {
                "reportKind": "aggregate",
                "releaseEligible": True,
                "releaseCandidate": evidence,
            }
        ),
        encoding="utf-8",
    )
    assert release_candidate.verify_eligibility_report(package_root, archive, report) == evidence

    report.write_text(
        json.dumps(
            {
                "reportKind": "lane",
                "releaseEligible": True,
                "releaseCandidate": evidence,
            }
        ),
        encoding="utf-8",
    )
    with pytest.raises(release_candidate.CandidateError, match="not an aggregate"):
        release_candidate.verify_eligibility_report(package_root, archive, report)

    (package_root / "VibeTable.Next.exe").write_bytes(b"changed")
    with pytest.raises(release_candidate.CandidateError, match="no longer matches"):
        release_candidate.verify_eligibility_report(package_root, archive, report)


def test_release_candidate_rejects_an_extra_top_level_directory(tmp_path: Path) -> None:
    archive = tmp_path / "VibeTable-v1.2.3-win-x64.zip"
    with zipfile.ZipFile(archive, "w") as output:
        output.writestr("VibeTable/app.bin", b"app")
        output.writestr("unexpected/", b"")

    with pytest.raises(release_candidate.CandidateError, match="top-level directory"):
        release_candidate.archive_tree(archive)


def test_upgrade_backup_is_outside_install_and_failure_keeps_old_binary(
    tmp_path: Path,
) -> None:
    install = tmp_path / "install"
    data = tmp_path / "user-data"
    install.mkdir()
    data.mkdir()
    old_binary = install / "vibetable-pb.exe"
    old_binary.write_bytes(b"old")
    (data / "data.db").write_bytes(b"db")

    transaction = prepare_upgrade(
        install_dir=install,
        data_dir=data,
        current_binary=old_binary,
    )

    assert transaction.backup_dir.parent == data.parent / "upgrade-backups"
    assert transaction.rollback_binary.read_bytes() == b"old"
    assert (transaction.backup_dir / "data" / "data.db").read_bytes() == b"db"
    assert old_binary.read_bytes() == b"old"
    assert os.path.commonpath([install, transaction.backup_dir]) != str(install)


def test_direct_release_script_can_prepare_an_upgrade(tmp_path: Path) -> None:
    install = tmp_path / "install"
    data = tmp_path / "user-data"
    install.mkdir()
    data.mkdir()
    current = install / "vibetable-pb.exe"
    current.write_bytes(b"current")

    result = subprocess.run(
        [
            sys.executable,
            str(REPO_ROOT / "scripts" / "release.py"),
            "--prepare-upgrade",
            "--install-dir",
            str(install),
            "--data-dir",
            str(data),
            "--current-binary",
            str(current),
        ],
        cwd=REPO_ROOT,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )

    assert result.returncode == 0, result.stderr
    assert Path(result.stdout.strip()).is_dir()


def test_release_preflight_rejects_dirty_or_untracked_worktree(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class Result:
        returncode = 0
        stdout = " M backend/app.py\n?? unexpected.bin\n"

    monkeypatch.setattr(
        "scripts.release.subprocess.run",
        lambda *args, **kwargs: Result(),
    )

    with pytest.raises(ValueError, match="clean worktree"):
        _ensure_clean_worktree()


def _legacy_release_workflow_contract() -> None:
    workflows = REPO_ROOT / ".github" / "workflows"
    workflow = (REPO_ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
    cleanup = (REPO_ROOT / ".github" / "workflows" / "release-cleanup.yml").read_text(
        encoding="utf-8"
    )
    release_please = (REPO_ROOT / ".github" / "workflows" / "release-please.yml").read_text(
        encoding="utf-8"
    )
    release_config = json.loads(
        (REPO_ROOT / "release-please-config.json").read_text(encoding="utf-8")
    )
    strategy_configs = {
        bump: json.loads(
            (REPO_ROOT / f"release-please-config-{bump}.json").read_text(encoding="utf-8")
        )
        for bump in ("patch", "minor", "major")
    }
    assert {path.name for path in workflows.glob("*.yml")} == {
        "ci.yml",
        "mirror.yml",
        "release-cleanup.yml",
        "release-please.yml",
        "release.yml",
    }

    assert "workflow_dispatch:" in workflow
    assert "release:\n    types: [published]" not in workflow
    # Build auto-fires after Release Please finishes so draft Releases get their
    # assets without a manual dispatch; workflow_run has no inputs, so the tag is
    # resolved dynamically instead of being read from inputs.release_tag.
    assert "workflow_run:" in workflow
    assert 'workflows: ["Release Please"]' in workflow
    assert "release_tag:" in workflow
    assert "Resolve release tag" in workflow
    assert "INPUT_TAG: ${{ inputs.release_tag }}" in workflow
    assert "RELEASE_TAG: ${{ needs.build.outputs.release_tag }}" in workflow
    # The final publish job consumes the build job's resolved immutable tag,
    # never the untrusted manual input directly.
    attach = workflow.split("Attach assets to draft Release", 1)[1]
    assert "${{ inputs.release_tag }}" not in attach
    assert "Verify tag and draft Release" in workflow
    assert "gh release view $env:RELEASE_TAG --json isDraft" in workflow
    assert "Attach assets to draft Release" in workflow
    assert "gh release upload" in workflow
    assert "Verify attached Release assets" in workflow
    assert "Publish verified Release" in workflow
    assert "github.event_name == 'workflow_run'" in workflow
    assert "gh release edit $env:RELEASE_TAG --draft=false --latest" in workflow
    assert "Keep the five most recent published Releases" not in workflow
    assert "release:\n    types: [published]" in cleanup
    assert "Keep the five most recent published Releases" in cleanup
    assert "gh api --paginate" in cleanup
    assert "--slurp" not in cleanup
    assert ".published_at, .id, .tag_name" in cleanup
    assert "LC_ALL=C sort -r" in cleanup
    assert "tail -n +6" in cleanup
    assert "databaseId" not in cleanup
    assert "schedule:" not in workflow
    assert "gh release create" not in workflow
    assert "git push" not in workflow

    assert "workflow_dispatch:" in release_please
    assert "options: [patch, minor, major]" in release_please
    assert "config-file: release-please-config-${{ inputs.bump }}.json" in release_please
    assert "Compute requested version" not in release_please
    assert "git commit --allow-empty" not in release_please
    assert "git push origin HEAD:main" not in release_please
    assert not (REPO_ROOT / ".github" / "release-request.json").exists()
    assert "release-request:" not in release_please
    assert "gh pr create" not in release_please
    assert "gh pr merge --auto --squash" not in release_please
    assert "Release-As:" not in release_please
    assert "if: github.event_name == 'workflow_dispatch'" in release_please
    assert "Clear stale pending labels from closed Release PRs" in release_please
    stale_label_cleanup = release_please.split(
        "Clear stale pending labels from closed Release PRs", 1
    )[1].split("Create or update Release PR", 1)[0]
    assert "gh pr list" in stale_label_cleanup
    assert "--state closed" in stale_label_cleanup
    assert "--base main" in stale_label_cleanup
    assert '--label "autorelease: pending"' in stale_label_cleanup
    assert 'gh pr edit "$number" --remove-label "autorelease: pending"' in release_please
    assert "skip-github-release: true" in release_please
    assert "skip-github-pull-request: true" in release_please
    assert "id: release" in release_please
    assert "RELEASE_PR: ${{ steps.release.outputs.pr }}" in release_please
    assert "python scripts/changelog.py --write" in release_please
    assert "desktop/web-grid/src/generated/changelog.json" in release_please
    refresh_changelog = release_please.split("Refresh generated changelog in Release PR", 1)[1]
    assert 'git config user.name "github-actions[bot]"' in refresh_changelog
    assert (
        'git config user.email "41898282+github-actions[bot]@users.noreply.github.com"'
        in refresh_changelog
    )
    assert refresh_changelog.index(
        'git config user.name "github-actions[bot]"'
    ) < refresh_changelog.index('git commit -m "chore(release): refresh generated changelog"')
    assert release_config["packages"]["."]["skip-changelog"] is True
    assert "versioning-strategy" not in release_config["packages"]["."]
    expected_strategies = {
        "patch": "always-bump-patch",
        "minor": "always-bump-minor",
        "major": "always-bump-major",
    }
    assert set(strategy_configs) == set(expected_strategies)
    for bump, strategy_config in strategy_configs.items():
        assert strategy_config["packages"]["."]["versioning-strategy"] == expected_strategies[bump]
        strategy_config["packages"]["."].pop("versioning-strategy")
        assert strategy_config == release_config
    assert "--generate-notes" not in workflow
    # RELEASE_TAG is resolved by the "Resolve release tag" step (supports both
    # manual dispatch and the Release Please workflow_run hook) and flows through
    # env to downstream steps; the Resolve step must run before asset attach.
    assert workflow.index("Resolve release tag") < workflow.index("Attach assets to draft Release")
    assert "w64devkit-x64-2.8.0.7z.exe" in workflow
    assert "6252bf34fe2231a55ac7f03d482b36d2c7c58697990551bba508102cfb3f342e" in workflow
    assert '7z x $archive "-o$destination" -y' in workflow
    assert workflow.index("Build immutable release candidate") < workflow.index(
        "Install pinned Windows race toolchain"
    )
    assert "qa/next.py --ci" not in workflow
    for lane in ("core", "race", "resilience", "release"):
        assert workflow.count(f"qa/next.py --lane {lane}") == 1
    assert "--package-root dist/VibeTable.Next" in workflow
    assert (
        "PACKAGE_ARCHIVE: dist/VibeTable-v${{ needs.build.outputs.version }}-win-x64.zip"
        in workflow
    )
    assert "--package-archive $env:PACKAGE_ARCHIVE" in workflow
    assert "--json-report build/qa/release-eligibility.json" in workflow
    assert "Upload release eligibility evidence" in workflow
    assert workflow.index("Build immutable release candidate") < workflow.index(
        "Archive immutable release candidate"
    )
    assert workflow.index("Archive immutable release candidate") < workflow.index(
        "Run core release eligibility lane"
    )
    assert workflow.index("Aggregate immutable candidate evidence") < workflow.index(
        "Verify eligibility is bound to the immutable candidate"
    )
    assert workflow.count("scripts/build_next.py --release") == 1
    assert "Compress-Archive" not in workflow
    assert "if: always() && needs.build.outputs.release_tag != ''" in workflow
    gate_job = workflow.split("  gate:", 1)[1].split("\n  release:", 1)[0]
    assert "needs: [build, core, race, resilience]" in gate_job
    assert "Reject failed or skipped release lanes" in gate_job
    assert "environment: release" not in gate_job
    assert "contents: write" not in gate_job
    release_job = workflow.split("  release:", 1)[1]
    assert "needs: [build, gate]" in release_job
    assert "needs.gate.result == 'success'" in release_job
    assert workflow.count("environment: release") == 1
    assert workflow.count("contents: write") == 1
    assert "actions/download-artifact@v8" in workflow
    assert "actions/upload-artifact@v7" in workflow
    assert "Release lanes did not all succeed" in workflow
    assert workflow.index(
        "Verify eligibility is bound to the immutable candidate"
    ) < workflow.index("Attach assets to draft Release")
    assert workflow.index("Attach assets to draft Release") < workflow.index(
        "Verify attached Release assets"
    )
    assert workflow.index("Verify attached Release assets") < workflow.index(
        "Publish verified Release"
    )


def _legacy_ci_workflow_contract() -> None:
    workflow = (REPO_ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
    python_job = workflow.split("\n  web:", maxsplit=1)[0]

    assert "fetch-depth: 0" in python_job
    assert python_job.index("fetch-depth: 0") < python_job.index("Version and package metadata")
    assert "Release changelog freshness" in python_job
    assert "startsWith(github.head_ref, 'release-please--branches--')" in python_job
    assert "uv run python scripts/changelog.py --check" in python_job


def test_fault_gate_targets_workspace_v2_durability_without_whole_backup() -> None:
    tests = set(fault_injection.GO_TESTS)
    packages = set(fault_injection.GO_PACKAGES)

    assert "WholeBackup" not in "\n".join(tests)
    assert {
        "TestCatalogFailureNeverPublishesPartialRecord",
        "TestJournalPersistenceFailureAfterMutationRollsBack",
        "TestApplyRejectsStalePlanAndUnsafeInventory",
        "TestInspectRejectsExcessivePathAndCompressionRatio",
        "TestPersistentCoordinatorFailsClosedUntilPreparedMutationResolved",
        "TestPersistentQueueRetriesIdempotentlyAcrossRestart",
        "TestRuntimeFailsClosedForIdentityParamsAndEpoch",
    } <= tests
    assert {
        "./internal/snapshot",
        "./internal/restore",
        "./internal/retention",
        "./internal/snapshotpkg",
        "./internal/writecoordinator",
        "./internal/replica",
        "./internal/workspacev2",
    } <= packages


def test_packaged_sidecar_matrix_smokes_v2_snapshot_package() -> None:
    matrix = (REPO_ROOT / "tests" / "integration" / "packaged_sidecar_matrix.py").read_text(
        encoding="utf-8"
    )

    assert "/api/vibetable/v1/backups" not in matrix
    assert "backup+restore" not in matrix
    assert "/api/vibetable/v2/capabilities" in matrix
    assert "workspace.v1_write_disabled" in matrix
    assert '"snapshot.request"' in matrix
    assert '"snapshot.export"' in matrix
    assert "snapshot-package-v2.vtsnapshot" in matrix
    assert 'data_dir = run_root / "data"' in matrix
    assert "matrix requires a fresh audit directory" in matrix


def test_handoff_dependencies_preserve_core_and_add_workspace_v2_evidence() -> None:
    dependencies = json.loads(
        (REPO_ROOT / "qa" / "handoff_dependencies.json").read_text(encoding="utf-8")
    )
    capabilities = [
        capability
        for stage in dependencies["sequence"]
        for capability in dependencies["capabilities"][stage]
    ]

    assert "release.workspace-snapshot-v2" in capabilities
    assert {
        "schema.catalog.v1",
        "query.port.v1",
        "mutation.kernel.v1",
        "formula.cel-v1",
        "relation.lookup.v1",
        "plugin.local-worker.v1",
    } <= set(capabilities)
    assert "release.backup-restore.v1" not in capabilities
    assert {
        "legacy.backup.absent.v2",
        "legacy.scheme-main-adoption.absent.v2",
        "legacy.global-data-root.absent.v2",
    } <= set(capabilities)
    assert dependencies["artifactFiles"]["schema"] == [
        "contracts/v2/product-contracts.schema.json",
        "contracts/schema-v2/schema.schema.json",
        "contracts/v2/contracts.schema.json",
    ]
    for fixtures in dependencies["fixtures"].values():
        for relative in fixtures:
            assert (REPO_ROOT / relative).is_file(), relative


def test_upgrade_validates_migration_copy_then_atomically_activates(
    tmp_path: Path,
) -> None:
    install = tmp_path / "install"
    data = tmp_path / "user-data"
    candidate_dir = tmp_path / "candidate"
    install.mkdir()
    data.mkdir()
    candidate_dir.mkdir()
    current = install / "vibetable-pb.exe"
    candidate = candidate_dir / "vibetable-pb.exe"
    current.write_bytes(b"old")
    candidate.write_bytes(b"new")
    (data / "data.db").write_bytes(b"old-db")
    transaction = prepare_upgrade(
        install_dir=install,
        data_dir=data,
        current_binary=current,
    )

    def migrate(binary: Path, copied_data: Path) -> None:
        assert binary == candidate.resolve()
        assert (copied_data / "data.db").read_bytes() == b"old-db"
        (copied_data / "migration-3.ok").write_text("ok", encoding="utf-8")

    activate_upgrade(
        transaction,
        install_dir=install,
        data_dir=data,
        current_binary=current,
        new_binary=candidate,
        validator=migrate,
    )

    assert current.read_bytes() == b"new"
    assert (data / "migration-3.ok").read_text(encoding="utf-8") == "ok"
    manifest = json.loads(transaction.manifest.read_text(encoding="utf-8"))
    assert manifest["activation"]["status"] == "committed"


def test_upgrade_migration_failure_automatically_keeps_old_binary_and_data(
    tmp_path: Path,
) -> None:
    install = tmp_path / "install"
    data = tmp_path / "user-data"
    candidate_dir = tmp_path / "candidate"
    install.mkdir()
    data.mkdir()
    candidate_dir.mkdir()
    current = install / "vibetable-pb.exe"
    candidate = candidate_dir / "vibetable-pb.exe"
    current.write_bytes(b"old")
    candidate.write_bytes(b"bad-new")
    (data / "data.db").write_bytes(b"old-db")
    transaction = prepare_upgrade(
        install_dir=install,
        data_dir=data,
        current_binary=current,
    )

    with pytest.raises(RuntimeError, match="migration failed"):
        activate_upgrade(
            transaction,
            install_dir=install,
            data_dir=data,
            current_binary=current,
            new_binary=candidate,
            validator=lambda _binary, _data: (_ for _ in ()).throw(
                RuntimeError("migration failed")
            ),
        )

    assert current.read_bytes() == b"old"
    assert (data / "data.db").read_bytes() == b"old-db"
    manifest = json.loads(transaction.manifest.read_text(encoding="utf-8"))
    assert manifest["activation"]["status"] == "rolledBack"


@pytest.mark.parametrize(
    ("role", "relative_path"),
    [
        ("failed-readiness", "self-update-readiness/vibetable-readiness.json"),
        ("rollback-receipt", None),
        ("restored-readiness", "self-update-restored-readiness/vibetable-readiness.json"),
        ("restored-state", "self-update-restored-controls/host-lifecycle-state.json"),
    ],
)
def test_self_update_rollback_reports_invalid_json_role_without_contents(
    tmp_path: Path, role: str, relative_path: str | None
) -> None:
    fixture = _write_strict_self_update_rollback_fixture(
        tmp_path,
        failure_code="workspaceHealthProbeFailed",
        health_failure_readiness={"ready": False, "error": "fixture failure"},
    )
    evidence_path = fixture.receipt_path if relative_path is None else tmp_path / relative_path
    evidence_path.write_text("{private-test-payload", encoding="utf-8")
    with pytest.raises(build_next.BuildError) as caught:
        build_next.wait_for_self_update_health_failure_rollback(
            tmp_path,
            process_scope=fixture.process_scope,
            target=fixture.target,
            stage=fixture.stage,
            token=fixture.token,
            updater_process_id=fixture.updater_process_id,
            updated_process_id=fixture.updated_process_id,
            timeout_seconds=0.1,
        )
    message = str(caught.value)
    assert role in message
    assert "JSONDecodeError line=1 column=2" in message
    assert "private-test-payload" not in message
    assert str(tmp_path) not in message
    assert fixture.token not in message


def test_self_update_rollback_reports_os_error_without_path(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fixture = _write_strict_self_update_rollback_fixture(
        tmp_path,
        failure_code="workspaceHealthProbeFailed",
        health_failure_readiness={"ready": False, "error": "fixture failure"},
    )
    original = Path.read_text

    def read_evidence(path: Path, *args: object, **kwargs: object) -> str:
        if path == fixture.receipt_path:
            raise PermissionError(13, "private-test-error", str(path))
        return original(path, *args, **kwargs)

    monkeypatch.setattr(Path, "read_text", read_evidence)
    with pytest.raises(build_next.BuildError) as caught:
        build_next.wait_for_self_update_health_failure_rollback(
            tmp_path,
            process_scope=fixture.process_scope,
            target=fixture.target,
            stage=fixture.stage,
            token=fixture.token,
            updater_process_id=fixture.updater_process_id,
            updated_process_id=fixture.updated_process_id,
            timeout_seconds=0.1,
        )
    message = str(caught.value)
    assert "rollback-receipt" in message
    assert "PermissionError errno=13" in message
    assert str(tmp_path) not in message
    assert "private-test-error" not in message
