from __future__ import annotations

import ast
import json
from pathlib import Path

import pytest

from tests.e2e import legacy_candidate_upgrade

ROOT = Path(__file__).resolve().parents[2]
METADATA = Path(__file__).with_name("legacy_candidate_upgrade.json")


def _upgrade_report(
    *,
    seeded: dict[str, int],
    clean: dict[str, int],
    rolled_back: dict[str, int],
    recovered: dict[str, int],
    snapshot_restored: dict[str, int],
) -> dict[str, object]:
    return {
        "ok": True,
        "evidenceKind": "packaged-sidecar-run",
        "seeded": {"database": {"counts": seeded}},
        "cleanUpgrade": {"database": {"counts": clean}},
        "rolledBack": {"counts": rolled_back},
        "recovered": {"database": {"counts": recovered}},
        "snapshotRestore": {"database": {"counts": snapshot_restored}},
    }


def test_upgrade_report_preserves_seeded_audit_counts_across_all_paths() -> None:
    baseline = {
        "vibetable_audit_events": 4,
        "vibetable_outbox": 4,
        "vibetable_audit_outbox": 11,
    }
    report = _upgrade_report(
        seeded=baseline,
        clean={**baseline, "vibetable_outbox": 14},
        rolled_back=baseline.copy(),
        recovered={**baseline, "vibetable_audit_events": 6},
        snapshot_restored={**baseline, "vibetable_audit_outbox": 16},
    )

    legacy_candidate_upgrade.validate_upgrade_report(report)


def test_upgrade_report_rejects_audit_loss_and_non_exact_fault_rollback() -> None:
    baseline = {
        "vibetable_audit_events": 4,
        "vibetable_outbox": 4,
        "vibetable_audit_outbox": 11,
    }
    lossy_upgrade = _upgrade_report(
        seeded=baseline,
        clean={**baseline, "vibetable_outbox": 3},
        rolled_back=baseline.copy(),
        recovered=baseline.copy(),
        snapshot_restored=baseline.copy(),
    )
    changed_rollback = _upgrade_report(
        seeded=baseline,
        clean=baseline.copy(),
        rolled_back={**baseline, "vibetable_audit_outbox": 12},
        recovered=baseline.copy(),
        snapshot_restored=baseline.copy(),
    )

    with pytest.raises(AssertionError, match=r"cleanUpgrade.*vibetable_outbox"):
        legacy_candidate_upgrade.validate_upgrade_report(lossy_upgrade)
    with pytest.raises(AssertionError, match="rolledBack counts changed"):
        legacy_candidate_upgrade.validate_upgrade_report(changed_rollback)


def test_workspace_copy_excludes_transient_sqlite_files(tmp_path: Path) -> None:
    source = tmp_path / "source"
    data = source / ".vibetable" / "data"
    data.mkdir(parents=True)
    (data / "data.db").write_bytes(b"durable")
    (data / "AUXILIARY.DB-SHM.tmp").write_bytes(b"transient")
    destination = tmp_path / "destination"

    legacy_candidate_upgrade.copy_workspace_for_upgrade(source, destination)

    assert (destination / ".vibetable" / "data" / "data.db").read_bytes() == b"durable"
    assert not (destination / ".vibetable" / "data" / "AUXILIARY.DB-SHM.tmp").exists()


def test_candidate_metadata_pins_real_schema_five_ancestor_and_rollback_boundary() -> None:
    metadata = json.loads(METADATA.read_text(encoding="utf-8"))

    assert metadata["formatVersion"] == 1
    assert metadata["candidate"] == {
        "commit": "ff95d26d827939335e4c903c889f00fc0b97abf1",
        "version": "0.1.0",
        "schemaVersion": 5,
        "packageRootEnvironment": "VIBETABLE_LEGACY_CANDIDATE_ROOT",
        "buildCommands": [
            ["uv", "sync", "--frozen", "--group", "dev", "--group", "build"],
            ["npm", "--prefix", "desktop/web-grid", "ci"],
            ["uv", "run", "python", "scripts/build_next.py", "--release"],
        ],
        "migrationFiles": [
            "2026072401_bootstrap.go",
            "2026072402_internal_collections.go",
            "2026072403_internal_metadata_channel.go",
            "2026072404_realtime_outbox_retention.go",
            "2026072805_audit_outbox.go",
        ],
    }
    assert metadata["target"]["pendingMigrations"] == [
        "2026072801_field_settings_v2_metadata.go",
        "2026080501_relation_pairs.go",
    ]
    assert metadata["rollbackBoundary"] == {
        "beforeNewSchemaWrites": "old-binary-with-pre-upgrade-copy",
        "afterNewSchemaWrites": "forward-fix-or-export-only",
    }


def test_harness_never_imports_release_activation_helpers() -> None:
    source = Path(legacy_candidate_upgrade.__file__).read_text(encoding="utf-8")
    imported = {
        alias.name
        for node in ast.walk(ast.parse(source))
        if isinstance(node, (ast.Import, ast.ImportFrom))
        for alias in node.names
    }

    assert "scripts.release" not in imported
    assert "prepare_upgrade" not in source
    assert "activate_upgrade" not in source


def test_release_smoke_command_is_bound_to_both_candidate_roots() -> None:
    command = legacy_candidate_upgrade.release_smoke_command(
        legacy_package_root=Path("legacy-package"),
        current_package_root=Path("current-package"),
        evidence_root=Path("evidence"),
    )

    assert command == [
        "uv",
        "run",
        "python",
        "-m",
        "tests.e2e.legacy_candidate_upgrade",
        "--legacy-package-root",
        "legacy-package",
        "--current-package-root",
        "current-package",
        "--evidence-root",
        "evidence",
        "--json-report",
        str(Path("evidence") / "report.json"),
    ]


def test_automation_release_smoke_owns_candidate_bound_stage() -> None:
    source = (ROOT / "scripts" / "automation_project.py").read_text(encoding="utf-8")

    assert "legacy_candidate_upgrade.release_smoke_command" in source
    assert "legacy_candidate_upgrade.ensure_legacy_candidate" in source
    assert "legacy_candidate_upgrade.current_candidate_root" in source


def test_default_legacy_candidate_source_is_built_from_the_fixed_commit() -> None:
    source = Path(legacy_candidate_upgrade.__file__).read_text(encoding="utf-8")

    assert '["git", "worktree", "add", "--detach", str(source_root), commit]' in source
    assert "for raw_command in commands" in source
    assert 'candidate["defaultPackageRoot"]' not in source


def test_clean_root_build_uses_declared_worktree_and_bootstrap_order(
    tmp_path: Path,
    monkeypatch,
) -> None:
    commit = "ff95d26d827939335e4c903c889f00fc0b97abf1"
    (tmp_path / ".worktrees").mkdir()
    source_root = tmp_path / ".worktrees" / "legacy-candidate-ff95d26d"
    calls: list[tuple[list[str], Path]] = []

    def fake_run(
        command: list[str],
        *,
        cwd: Path,
        check: bool,
        capture_output: bool = False,
        text: bool = False,
        encoding: str | None = None,
    ):
        del check, capture_output, text, encoding
        calls.append((command, cwd))
        if command[:3] == ["git", "worktree", "add"]:
            source_root.mkdir(parents=True)
        if command[-2:] == ["scripts/build_next.py", "--release"]:
            (source_root / "dist" / "VibeTable.Next").mkdir(parents=True)
        stdout = commit + "\n" if command == ["git", "rev-parse", "HEAD"] else ""
        return legacy_candidate_upgrade.subprocess.CompletedProcess(command, 0, stdout)

    monkeypatch.delenv("VIBETABLE_LEGACY_CANDIDATE_ROOT", raising=False)
    monkeypatch.setattr(legacy_candidate_upgrade.subprocess, "run", fake_run)

    result = legacy_candidate_upgrade.ensure_legacy_candidate(tmp_path)

    assert result == source_root / "dist" / "VibeTable.Next"
    assert [command for command, _cwd in calls] == [
        ["git", "worktree", "add", "--detach", str(source_root), commit],
        ["git", "rev-parse", "HEAD"],
        ["uv", "sync", "--frozen", "--group", "dev", "--group", "build"],
        ["npm", "--prefix", "desktop/web-grid", "ci"],
        ["uv", "run", "python", "scripts/build_next.py", "--release"],
    ]
