from __future__ import annotations

import hashlib
import hmac
import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

from qa import handoff, package_check, provider_evidence_check, release_candidate

ATTESTATION_KEY = "lab-secret"
ATTESTATION_KEY_ID = "release-lab-v1"


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


def _source_identity(root: Path) -> str:
    source = root / "product/source.py"
    source.parent.mkdir(parents=True, exist_ok=True)
    source.write_text("VALUE = 1\n", encoding="utf-8")
    dependencies = {
        "releaseIdentityInputs": ["product"],
        "releaseIdentityExcludedDirectories": ["provider-evidence"],
        "releaseIdentityExtensions": [".py"],
    }
    manifest = root / "qa/handoff_dependencies.json"
    manifest.parent.mkdir(parents=True, exist_ok=True)
    manifest.write_text(json.dumps(dependencies), encoding="utf-8")
    return handoff.release_source_hash(dependencies, repo_root=root)


def _dependency_versions(root: Path) -> dict[str, str]:
    go_mod = root / "tools/recovery-tools/go.mod"
    go_mod.parent.mkdir(parents=True, exist_ok=True)
    go_mod.write_text(
        "\n".join(
            (
                "module vibetable.local/recovery-tools",
                "",
                "go 1.25.8",
                "",
                "require (",
                "\tfilippo.io/age v1.3.1",
                "\tgithub.com/kopia/kopia v0.23.1",
                ")",
                "",
            )
        ),
        encoding="utf-8",
    )
    lock = root / "tools/workspace-storage-dependencies.json"
    lock.write_text(
        json.dumps(
            {
                "schemaVersion": 1,
                "dependencies": {
                    "sqlite": {
                        "module": "modernc.org/sqlite",
                        "version": "v1.54.0",
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    return {
        "age": "v1.3.1",
        "go": "1.25.8",
        "kopia": "v0.23.1",
        "sqlite": "v1.54.0",
    }


def _canonical(payload: object) -> bytes:
    return json.dumps(
        payload,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
    ).encode()


def _release_artifacts(
    root: Path,
    *,
    content: bytes = b"release candidate",
) -> tuple[dict[str, str], Path]:
    package_root = root / "VibeTable.Next"
    artifact = package_root / "app.bin"
    artifact.parent.mkdir(parents=True, exist_ok=True)
    artifact.write_bytes(content)
    archive = root / "VibeTable.Next.zip"
    evidence = release_candidate.create_archive(package_root, archive)
    archive_evidence = evidence["archive"]
    assert isinstance(archive_evidence, dict)
    return (
        {
            "packageTree": str(evidence["packageTreeSha256"]),
            str(archive_evidence["name"]): str(archive_evidence["sha256"]),
        },
        artifact,
    )


def _evidence(
    root: Path,
    *,
    source_commit: str,
    source_hash: str,
    artifact_hashes: dict[str, str],
    expires_at: datetime,
    dependencies: dict[str, str],
) -> None:
    evidence_root = root / "qa/provider-evidence"
    logs = evidence_root / "logs"
    logs.mkdir(parents=True)
    log = b"hardware lab passed\n"
    log_hash = hashlib.sha256(log).hexdigest()
    (logs / f"{log_hash}.log").write_bytes(log)
    generated_at = expires_at - timedelta(days=1)
    payload = {
        "schemaVersion": 1,
        "evidenceId": "hardware.smb-v1",
        "provider": "network",
        "sourceCommit": source_commit,
        "sourceHash": source_hash,
        "artifactHashes": artifact_hashes,
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
    evidence_path = evidence_root / "hardware.smb-v1.json"
    evidence_path.write_text(
        json.dumps(payload),
        encoding="utf-8",
    )
    claims = {
        "schemaVersion": 1,
        "evidenceSha256": hashlib.sha256(evidence_path.read_bytes()).hexdigest(),
        "dependencies": dependencies,
        "keyId": ATTESTATION_KEY_ID,
    }
    attestation = {
        **claims,
        "signature": hmac.new(
            ATTESTATION_KEY.encode(),
            _canonical(claims),
            hashlib.sha256,
        ).hexdigest(),
    }
    (evidence_root / "hardware.smb-v1.attestation.json").write_text(
        json.dumps(attestation),
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
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    assert (
        package_check.check_packaged_provider_support(
            package / "contracts/v2/provider-support.json",
            source,
            now=now,
            expected_artifact_hashes=artifact_hashes,
        )
        == []
    )


def test_source_check_accepts_authentic_evidence_before_candidate_exists(
    tmp_path: Path,
    monkeypatch,
) -> None:
    _support(tmp_path, creation="enabled")
    head = "a" * 40
    now = datetime(2026, 7, 29, tzinfo=UTC)
    source_hash = _source_identity(tmp_path)
    dependencies = _dependency_versions(tmp_path)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "future-release")
    _evidence(
        tmp_path,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    assert provider_evidence_check.check(tmp_path, now=now) == []


def test_packaged_provider_requires_candidate_artifact_hashes(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    head = "a" * 40
    now = datetime(2026, 7, 29, tzinfo=UTC)
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
    )

    assert "hardware.smb-v1: release candidate artifact hashes are unavailable" in errors


def test_packaged_enabled_provider_rejects_forged_source_tree(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    head = "a" * 40
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    (source / "product/source.py").write_text("VALUE = 2\n", encoding="utf-8")
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
        expected_artifact_hashes=artifact_hashes,
    )

    assert "hardware.smb-v1: sourceHash does not match the current source tree" in errors


def test_packaged_enabled_provider_rejects_forged_release_artifact(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    head = "a" * 40
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    forged_hashes, _artifact = _release_artifacts(
        tmp_path / "release",
        content=b"forged release candidate",
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
        expected_artifact_hashes=forged_hashes,
    )

    assert "hardware.smb-v1: artifactHashes do not match the release candidate" in errors


def test_packaged_enabled_provider_rejects_expired_evidence(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    head = "a" * 40
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now - timedelta(seconds=1),
        dependencies=dependencies,
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
        expected_artifact_hashes=artifact_hashes,
    )

    assert "hardware.smb-v1: evidence is future-dated or expired" in errors


def test_packaged_enabled_provider_rejects_attestation_with_missing_dependency_version(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    head = "a" * 40
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies={key: value for key, value in dependencies.items() if key != "age"},
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", ATTESTATION_KEY)
    monkeypatch.setenv("VIBETABLE_PROVIDER_EVIDENCE_KEY_ID", ATTESTATION_KEY_ID)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
        expected_artifact_hashes=artifact_hashes,
    )

    assert "hardware.smb-v1: attested dependency/tool versions do not match source locks" in errors


def test_enabled_provider_rejects_untrusted_attestation_without_protected_key(
    tmp_path: Path,
    monkeypatch,
) -> None:
    source = tmp_path / "source"
    package = tmp_path / "package"
    _support(package, creation="enabled")
    now = datetime(2026, 7, 29, tzinfo=UTC)
    head = "a" * 40
    source_hash = _source_identity(source)
    dependencies = _dependency_versions(source)
    artifact_hashes, _artifact = _release_artifacts(tmp_path / "release")
    _evidence(
        source,
        source_commit=head,
        source_hash=source_hash,
        artifact_hashes=artifact_hashes,
        expires_at=now + timedelta(days=1),
        dependencies=dependencies,
    )
    monkeypatch.setattr(provider_evidence_check, "_head", lambda _root: head)
    monkeypatch.delenv("VIBETABLE_PROVIDER_EVIDENCE_HMAC_KEY", raising=False)

    errors = package_check.check_packaged_provider_support(
        package / "contracts/v2/provider-support.json",
        source,
        now=now,
        expected_artifact_hashes=artifact_hashes,
    )

    assert "hardware.smb-v1: protected attestation key is unavailable" in errors


def test_repository_policy_blocks_every_unverified_provider() -> None:
    assert (
        provider_evidence_check.check(
            provider_evidence_check.PROJECT_ROOT,
            now=datetime(2026, 7, 28, tzinfo=UTC),
        )
        == []
    )
