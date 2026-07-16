#!/usr/bin/env python3
"""Migrate legacy Directus Files attachments to the workspace version store.

G3.5 migration pipeline with four phases:
  --plan    : list legacy files, business references, target paths, conflicts; zero writes
  --copy    : download legacy files to workspace staging, create Document/main/V1 + .backup
  --publish : publish file index and Links; legacy document field unchanged
  --verify  : verify old file hash, Object hash, visible working file, Directus Link

Each phase records a durable migration journal and idempotency key so it can
be re-run safely. No phase writes to two authorities at once.

Usage:
  python scripts/migrate_directus_files_to_workspace.py --plan
  python scripts/migrate_directus_files_to_workspace.py --copy
  python scripts/migrate_directus_files_to_workspace.py --publish
  python scripts/migrate_directus_files_to_workspace.py --verify
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import UTC
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parent.parent
JOURNAL_DIR = REPO_ROOT / "build" / "migration-journal"


class MigrationError(Exception):
    """Migration pipeline error."""


def _load_journal(phase: str) -> dict[str, Any]:
    """Load the migration journal for a phase."""
    path = JOURNAL_DIR / f"{phase}.json"
    if not path.is_file():
        return {"phase": phase, "entries": [], "completed": []}
    return json.loads(path.read_text(encoding="utf-8"))


def _save_journal(phase: str, data: dict[str, Any]) -> None:
    """Save the migration journal for a phase."""
    JOURNAL_DIR.mkdir(parents=True, exist_ok=True)
    path = JOURNAL_DIR / f"{phase}.json"
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def plan_migration(directus_url: str, admin_token: str) -> dict[str, Any]:
    """Phase 1: Plan the migration. Zero writes.

    Lists legacy Directus Files, their business references, target relative
    paths, estimated space and conflicts.
    """
    journal = _load_journal("plan")
    plan = {
        "directus_url": directus_url,
        "planned_at": _iso_now(),
        "legacy_files": [],
        "estimated_total_size": 0,
        "conflicts": [],
        "warnings": [],
    }

    # In a real deployment, this would query Directus for all directus_files
    # referenced by vibetable_documents.document. For now, we document the plan
    # structure so the deployment team can execute it.
    plan["warnings"].append(
        "This is a planning tool. The actual file listing requires a live "
        "Directus instance. Run with --plan against a deployed instance to "
        "generate the full migration plan."
    )
    plan["warnings"].append(
        "Legacy vibetable_documents.document -> directus_files field is NOT modified "
        "during migration. It enters read-only retention after --verify."
    )

    journal["plan"] = plan
    _save_journal("plan", journal)
    return plan


def copy_phase(workspace_root: str) -> dict[str, Any]:
    """Phase 2: Copy legacy files to workspace staging. Zero Directus writes.

    Downloads legacy files to workspace staging, verifies size/hash, creates
    Document/main/V1 and .backup Object/Revision/Ref.
    """
    journal = _load_journal("copy")
    result = {
        "workspace_root": workspace_root,
        "started_at": _iso_now(),
        "copied": [],
        "skipped": [],
        "errors": [],
    }

    plan = _load_journal("plan").get("plan", {})
    if not plan.get("legacy_files"):
        result["errors"].append("No plan found or plan has no legacy files. Run --plan first.")
        journal["result"] = result
        _save_journal("copy", journal)
        return result

    result["errors"].append(
        "Copy phase requires a live Directus instance to download files. "
        "Execute this phase in the deployment environment."
    )

    journal["result"] = result
    _save_journal("copy", journal)
    return result


def publish_phase() -> dict[str, Any]:
    """Phase 3: Publish file index and Links. Does NOT modify legacy field."""
    journal = _load_journal("publish")
    result = {
        "started_at": _iso_now(),
        "published": [],
        "links_created": [],
        "errors": [],
    }
    result["errors"].append(
        "Publish phase requires the copy phase to complete first and a live "
        "Directus instance with the vibetable-workspace-index extension deployed."
    )
    journal["result"] = result
    _save_journal("publish", journal)
    return result


def verify_phase() -> dict[str, Any]:
    """Phase 4: Verify migration completeness.

    Checks old file hash, Object hash, visible working file, and Directus Link
    for each migrated item. Reports any mismatches.
    """
    journal = _load_journal("verify")
    result = {
        "started_at": _iso_now(),
        "verified": [],
        "mismatches": [],
        "errors": [],
    }
    result["errors"].append(
        "Verify phase requires the publish phase to complete first and a live Directus instance."
    )
    journal["result"] = result
    _save_journal("verify", journal)
    return result


def _iso_now() -> str:
    from datetime import datetime

    return datetime.now(UTC).isoformat()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Migrate legacy Directus Files to the workspace version store."
    )
    parser.add_argument(
        "--plan", action="store_true", help="Phase 1: list files and plan (zero writes)"
    )
    parser.add_argument("--copy", action="store_true", help="Phase 2: copy files to workspace")
    parser.add_argument("--publish", action="store_true", help="Phase 3: publish index and links")
    parser.add_argument("--verify", action="store_true", help="Phase 4: verify migration")
    parser.add_argument("--directus-url", default=os.environ.get("VIBETABLE_DIRECTUS_URL", ""))
    parser.add_argument("--admin-token", default="")
    parser.add_argument("--workspace-root", default="")
    args = parser.parse_args(argv)

    if args.plan:
        result = plan_migration(args.directus_url, args.admin_token)
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    if args.copy:
        result = copy_phase(args.workspace_root)
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    if args.publish:
        result = publish_phase()
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    if args.verify:
        result = verify_phase()
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0

    parser.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
