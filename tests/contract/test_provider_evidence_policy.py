from __future__ import annotations

import hashlib
import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

from qa import package_check, provider_evidence_check


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


def _evidence(root: Path, *, source_commit: str, expires_at: datetime) -> None:
    evidence_root = root / "qa/provider-evidence"
    logs = evidence_root / "logs"
    logs.mkdir(parents=True)
    log = b"hardware lab passed\n"
    log_hash = hashlib.sha256(log).hexdigest()
    (logs / f"{log_hash}.log").write_bytes(log)
    generated_at = expires_at - timedelta(days=1)
    (evidence_root / "hardware.smb-v1.json").write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "evidenceId": "hardware.smb-v1",
                "provider": "network",
                "sourceCommit": source_commit,
                "sourceHash": "b" * 64,
                "artifactHashes": {"package.zip": "c" * 64},
                "generatedAt": generated_at.isoformat(),
                "expiresAt": expires_at.isoformat(),
                "releaseEligible": True,
                "runs": [
                    {
                        "stage": "durable rename",
                        "oracle": "workspace remains consistent",
                        "timeoutSeconds": 60,
                        "result": "passed",
                        "logSha256": log_hash,
                    }
                ],
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


def test_packaged_enabled_provider_accepts_current_authentic_evidence(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    head = "a" * 40
    now = datetime(2026, 7, 29, tzinfo=UTC)
    _evidence(source, source_commit=head, expires_at=now + timedelta(days=1))
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)

    assert (
        package_check.check_packaged_provider_support(
            package / "contracts/v2/provider-support.json",
            source,
            now=now,
        )
        == []
    )


def test_packaged_enabled_provider_rejects_forged_or_expired_evidence(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    _evidence(source, source_commit="b" * 40, expires_at=now - timedelta(seconds=1))
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: "a" * 40)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
    )

    assert "hardware.smb-v1: evidence is not bound to current commit" in errors
    assert "hardware.smb-v1: evidence is future-dated or expired" in errors


def test_repository_policy_blocks_every_unverified_provider() -> None:
    assert (
        provider_evidence_check.check(
            provider_evidence_check.PROJECT_ROOT,
            now=datetime(2026, 7, 28, tzinfo=UTC),
        )
        == []
    )
