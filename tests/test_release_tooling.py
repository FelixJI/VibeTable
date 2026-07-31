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

import pytest

from qa import fault_injection, package_check, release_candidate
from qa.package_check import check_package
from scripts import build_next
from scripts.release import (
    _ensure_clean_worktree,
    activate_upgrade,
    prepare_upgrade,
)
from scripts.versioning import (
    bump_version,
    check_versions,
    collect_release_versions,
    read_project_version,
    update_versions,
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
        "workspaceFormat": "1",
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
    assert versions.pocketbase == "0.39.9"
    assert versions.cel == "0.26.1"
    assert versions.contract == "v1"
    assert versions.schema == "6"
    assert len(versions.migration_hash) == 64


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
    assert relative == {"backend/_version.py", "desktop/publish-layout.json"}
    assert all(removed_provider not in item.lower() for item in relative)
    assert read_project_version(REPO_ROOT) == original


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
        "pocketBaseVersion": "0.39.9",
        "celVersion": "0.26.1",
        "contractVersion": "2.0",
        "schemaVersion": "6",
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
        "workspace": 1,
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
            ("github.com/pocketbase/pocketbase", "v0.39.9"),
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
            ("github.com/pocketbase/pocketbase", "v0.39.9"),
            ("github.com/google/cel-go", "v0.26.1"),
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
        json.dumps({"releaseEligible": True, "releaseCandidate": evidence}),
        encoding="utf-8",
    )
    assert release_candidate.verify_eligibility_report(package_root, archive, report) == evidence

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


def test_release_workflows_use_manual_bumps_and_only_fill_draft_releases() -> None:
    workflow = (REPO_ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
    release_please = (
        REPO_ROOT / ".github" / "workflows" / "release-please.yml"
    ).read_text(encoding="utf-8")

    assert "workflow_dispatch:" in workflow
    assert "release_tag:" in workflow
    assert "Verify tag and draft Release" in workflow
    assert "gh release view $env:RELEASE_TAG --json isDraft" in workflow
    assert "Attach assets to draft Release" in workflow
    assert "gh release upload" in workflow
    assert "schedule:" not in workflow
    assert "gh release create" not in workflow
    assert "git push" not in workflow

    assert "workflow_dispatch:" in release_please
    assert "options: [patch, minor, major]" in release_please
    assert "release-as:" in release_please
    assert "skip-github-release: true" in release_please
    assert "skip-github-pull-request: true" in release_please
    assert "--generate-notes" not in workflow
    assert "RELEASE_TAG: ${{ inputs.release_tag }}" in workflow
    assert "w64devkit-x64-2.8.0.7z.exe" in workflow
    assert "6252bf34fe2231a55ac7f03d482b36d2c7c58697990551bba508102cfb3f342e" in workflow
    assert '7z x $archive "-o$destination" -y' in workflow
    assert workflow.index("Install pinned Windows race toolchain") < workflow.index(
        "Build immutable release candidate"
    )
    assert "qa/next.py --ci" in workflow
    assert "--package-root dist/VibeTable.Next" in workflow
    assert (
        "PACKAGE_ARCHIVE: dist/VibeTable-v${{ steps.identity.outputs.version }}-win-x64.zip"
        in workflow
    )
    assert "--package-archive $env:PACKAGE_ARCHIVE" in workflow
    assert "--json-report build/qa/release-eligibility.json" in workflow
    assert "Upload release eligibility evidence" in workflow
    assert workflow.index("Build immutable release candidate") < workflow.index(
        "Archive immutable release candidate"
    )
    assert workflow.index("Archive immutable release candidate") < workflow.index(
        "Run complete release eligibility gate"
    )
    assert workflow.index("Run complete release eligibility gate") < workflow.index(
        "Verify eligibility is bound to the immutable candidate"
    )
    assert workflow.count("scripts/build_next.py --release") == 1
    assert "Compress-Archive" not in workflow
    assert workflow.index(
        "Verify eligibility is bound to the immutable candidate"
    ) < workflow.index("Attach assets to draft Release")


def test_ci_metadata_checkout_fetches_release_tags() -> None:
    workflow = (REPO_ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
    python_job = workflow.split("\n  web:", maxsplit=1)[0]

    assert "fetch-depth: 0" in python_job
    assert python_job.index("fetch-depth: 0") < python_job.index("Version and package metadata")


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
        "contracts/v1/contracts.schema.json",
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
