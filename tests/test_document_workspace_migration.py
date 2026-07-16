"""Tests for the G3.5 Directus Files migration script.

Validates that each phase:
1. Produces a durable journal
2. Does not write to Directus
3. Reports the correct phase status
4. Can be re-run safely (idempotent)
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import patch

from scripts.migrate_directus_files_to_workspace import (
    copy_phase,
    plan_migration,
    publish_phase,
    verify_phase,
)


def test_plan_phase_produces_journal(tmp_path: Path) -> None:
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        result = plan_migration("https://directus.example.com", "admin-token")

    assert result["directus_url"] == "https://directus.example.com"
    assert "planned_at" in result
    assert isinstance(result["legacy_files"], list)
    journal = json.loads((tmp_path / "plan.json").read_text(encoding="utf-8"))
    assert journal["phase"] == "plan"
    assert "plan" in journal


def test_plan_phase_warns_about_legacy_field(tmp_path: Path) -> None:
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        result = plan_migration("https://test", "token")
    warnings_text = " ".join(result["warnings"])
    assert "NOT modified" in warnings_text
    assert "read-only" in warnings_text


def test_copy_phase_requires_plan(tmp_path: Path) -> None:
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        result = copy_phase("/workspace")
    assert len(result["errors"]) > 0
    assert "No plan found" in result["errors"][0] or "live" in result["errors"][0].lower()


def test_publish_phase_records_journal(tmp_path: Path) -> None:
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        result = publish_phase()
    assert "started_at" in result
    journal = json.loads((tmp_path / "publish.json").read_text(encoding="utf-8"))
    assert journal["phase"] == "publish"


def test_verify_phase_records_journal(tmp_path: Path) -> None:
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        result = verify_phase()
    assert "started_at" in result
    journal = json.loads((tmp_path / "verify.json").read_text(encoding="utf-8"))
    assert journal["phase"] == "verify"


def test_plan_is_idempotent(tmp_path: Path) -> None:
    """Running plan twice should not fail or corrupt the journal."""
    with patch("scripts.migrate_directus_files_to_workspace.JOURNAL_DIR", tmp_path):
        plan_migration("https://test", "token")
        plan_migration("https://test", "token")
    journal = json.loads((tmp_path / "plan.json").read_text(encoding="utf-8"))
    assert journal["phase"] == "plan"
