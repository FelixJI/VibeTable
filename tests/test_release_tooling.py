from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path

import pytest

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


def test_repository_versions_are_consistent() -> None:
    assert check_versions(REPO_ROOT) == []
    versions = collect_release_versions(REPO_ROOT)
    assert versions.pocketbase == "0.39.9"
    assert versions.cel == "0.26.1"
    assert versions.contract == "v1"
    assert versions.schema == "4"
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
    assert "pyproject.toml" in relative
    assert "desktop/publish-layout.json" in relative
    assert all(removed_provider not in item.lower() for item in relative)
    assert read_project_version(REPO_ROOT) == original


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
        "contractVersion": "v1",
        "schemaVersion": "4",
        "migrationHash": collect_release_versions(REPO_ROOT).migration_hash,
        "sha256": digest,
    }
    assert manifest["launch"]["sidecar"] == "sidecar/vibetable-pb.exe"
    assert manifest["assets"]["migrations"] == "sidecar/migrations/manifest.json"
    assert manifest["assets"]["sbom"] == "sidecar/sbom.cdx.json"
    assert manifest["data"]["rootPolicy"] == "per-user-local-app-data"
    encoded = json.dumps(manifest).lower()
    assert "".join(["di", "rectus"]) not in encoded
    assert "node_modules" not in encoded
    assert "npm" not in encoded


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
    assert "buildinfo.Version=1.0.0" in ldflags
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
    build_info = {
        "version": "1.0.0",
        "pocketBaseVersion": "0.39.9",
        "celVersion": "0.26.1",
        "contractVersion": "v1",
        "schemaVersion": "4",
        "migrationHash": collect_release_versions(REPO_ROOT).migration_hash,
    }
    license_dir = tmp_path / "pocketbase-module"
    license_dir.mkdir()
    (license_dir / "LICENSE").write_text(
        "Mozilla Public License Version 2.0\n",
        encoding="utf-8",
    )

    build_next.stage_sidecar_assets(
        paths,
        build_info=build_info,
        modules=[
            {
                "path": "github.com/pocketbase/pocketbase",
                "version": "v0.39.9",
                "license": "MPL-2.0",
                "dir": str(license_dir),
            }
        ],
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
    license_dir = tmp_path / "dependency-module"
    license_dir.mkdir()
    (license_dir / "LICENSE").write_text(
        "Mozilla Public License Version 2.0\n",
        encoding="utf-8",
    )
    build_next.stage_sidecar_assets(
        stage,
        build_info={
            "version": "1.0.0",
            "pocketBaseVersion": "0.39.9",
            "celVersion": "0.26.1",
            "contractVersion": "v1",
            "schemaVersion": "4",
            "migrationHash": collect_release_versions(REPO_ROOT).migration_hash,
        },
        modules=[
            {
                "path": "example.invalid/dependency",
                "version": "v1.0.0",
                "license": "MPL-2.0",
                "dir": str(license_dir),
            }
        ],
    )
    build_next.write_manifest(stage)
    stage.sidecar_binary.write_bytes(b"tampered")

    with pytest.raises(build_next.BuildError, match="SHA-256"):
        build_next.verify_sidecar_package(stage)


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


def test_release_preflight_rejects_dirty_or_untracked_worktree(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class Result:
        stdout = " M backend/app.py\n?? unexpected.bin\n"

    monkeypatch.setattr(
        "scripts.release.subprocess.run",
        lambda *args, **kwargs: Result(),
    )

    with pytest.raises(ValueError, match="clean worktree"):
        _ensure_clean_worktree()


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
