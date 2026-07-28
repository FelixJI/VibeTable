from __future__ import annotations

import json
from datetime import UTC, datetime
from pathlib import Path

from qa import provider_evidence_check


def _support(root: Path, *, creation: str) -> None:
    path = root / "contracts/v2/provider-support.json"
    path.parent.mkdir(parents=True)
    path.write_text(
        json.dumps(
            {
                "contractVersion": "2.0",
                "policyRevision": 1,
                "evidenceContract": "provider-lab-evidence.schema.json",
                "evidenceDirectory": "qa/provider-evidence",
                "providers": {
                    "fixed": {
                        "creation": "enabled",
                        "coordinationStrength": "strong",
                        "evidence": ["automated.fixed"],
                    },
                    "network": {
                        "creation": creation,
                        "coordinationStrength": "advisory",
                        "requiredEvidence": "hardware.smb-v1",
                    },
                },
            }
        ),
        encoding="utf-8",
    )


def test_blocked_provider_needs_no_fabricated_hardware_evidence(tmp_path: Path) -> None:
    _support(tmp_path, creation="blockedPendingLab")
    assert provider_evidence_check.check(tmp_path) == []


def test_enabled_provider_without_evidence_is_release_blocked(tmp_path: Path) -> None:
    _support(tmp_path, creation="enabled")
    assert provider_evidence_check.check(tmp_path) == [
        "network: enabled without hardware.smb-v1 evidence"
    ]


def test_repository_policy_blocks_every_unverified_provider() -> None:
    assert (
        provider_evidence_check.check(
            provider_evidence_check.PROJECT_ROOT,
            now=datetime(2026, 7, 28, tzinfo=UTC),
        )
        == []
    )
