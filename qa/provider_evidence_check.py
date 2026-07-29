#!/usr/bin/env python3
"""Fail closed when an enabled non-fixed provider lacks current lab evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

PROJECT_ROOT = Path(__file__).resolve().parents[1]
SUPPORT_PATH = PROJECT_ROOT / "contracts" / "v2" / "provider-support.json"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
COMMIT = re.compile(r"^[0-9a-f]{40}$")


def _timestamp(value: Any) -> datetime:
    if not isinstance(value, str):
        raise ValueError("timestamp must be a string")
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp must include a timezone")
    return parsed.astimezone(UTC)


def _head(root: Path) -> str:
    return subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=root,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    ).stdout.strip()


def _validate_evidence(
    payload: dict[str, Any],
    *,
    evidence_id: str,
    provider: str,
    head: str,
    now: datetime,
    evidence_root: Path,
) -> list[str]:
    errors: list[str] = []
    exact = {
        "schemaVersion",
        "evidenceId",
        "provider",
        "sourceCommit",
        "sourceHash",
        "artifactHashes",
        "generatedAt",
        "expiresAt",
        "releaseEligible",
        "runs",
    }
    if set(payload) != exact:
        errors.append(f"{evidence_id}: evidence fields are not exact")
        return errors
    if payload["schemaVersion"] != 1:
        errors.append(f"{evidence_id}: unsupported schemaVersion")
    if payload["evidenceId"] != evidence_id or payload["provider"] != provider:
        errors.append(f"{evidence_id}: provider identity mismatch")
    if payload["sourceCommit"] != head or not COMMIT.fullmatch(payload["sourceCommit"]):
        errors.append(f"{evidence_id}: evidence is not bound to current commit")
    if not isinstance(payload["sourceHash"], str) or not SHA256.fullmatch(payload["sourceHash"]):
        errors.append(f"{evidence_id}: invalid sourceHash")
    hashes = payload["artifactHashes"]
    if (
        not isinstance(hashes, dict)
        or not hashes
        or any(
            not isinstance(key, str)
            or not key
            or not isinstance(value, str)
            or not SHA256.fullmatch(value)
            for key, value in hashes.items()
        )
    ):
        errors.append(f"{evidence_id}: invalid artifactHashes")
    try:
        generated = _timestamp(payload["generatedAt"])
        expires = _timestamp(payload["expiresAt"])
        if generated > now or expires <= now or expires <= generated:
            errors.append(f"{evidence_id}: evidence is future-dated or expired")
    except (TypeError, ValueError):
        errors.append(f"{evidence_id}: invalid evidence timestamps")
    if payload["releaseEligible"] is not True:
        errors.append(f"{evidence_id}: releaseEligible is not true")
    runs = payload["runs"]
    if not isinstance(runs, list) or not runs:
        errors.append(f"{evidence_id}: no hardware runs")
    else:
        required = {"stage", "oracle", "timeoutSeconds", "result", "logSha256"}
        for index, run in enumerate(runs):
            if not isinstance(run, dict) or set(run) != required:
                errors.append(f"{evidence_id}: run {index} fields are not exact")
                continue
            if (
                not isinstance(run["stage"], str)
                or not run["stage"]
                or not isinstance(run["oracle"], str)
                or not run["oracle"]
                or not isinstance(run["timeoutSeconds"], int)
                or isinstance(run["timeoutSeconds"], bool)
                or run["timeoutSeconds"] < 1
                or run["result"] != "passed"
                or not isinstance(run["logSha256"], str)
                or not SHA256.fullmatch(run["logSha256"])
            ):
                errors.append(f"{evidence_id}: run {index} is invalid")
            log = evidence_root / "logs" / f"{run['logSha256']}.log"
            if not log.is_file():
                errors.append(f"{evidence_id}: run {index} log is missing")
            elif hashlib.sha256(log.read_bytes()).hexdigest() != run["logSha256"]:
                errors.append(f"{evidence_id}: run {index} log hash mismatch")
    return errors


def check(
    root: Path = PROJECT_ROOT,
    *,
    now: datetime | None = None,
    support_path: Path | None = None,
) -> list[str]:
    root = root.resolve()
    selected_support_path = (
        support_path.resolve()
        if support_path is not None
        else root / "contracts" / "v2" / "provider-support.json"
    )
    try:
        support = json.loads(selected_support_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"provider support matrix is invalid: {exc}"]
    if support.get("contractVersion") != "2.0":
        return ["provider support contractVersion must be 2.0"]
    evidence_directory = support.get("evidenceDirectory")
    if evidence_directory != "qa/provider-evidence":
        return ["provider evidenceDirectory must be qa/provider-evidence"]
    evidence_root = root / evidence_directory
    providers = support.get("providers")
    if not isinstance(providers, dict):
        return ["provider support matrix has no providers"]
    current = now or datetime.now(UTC)
    head: str | None = None
    errors: list[str] = []
    for provider, policy in providers.items():
        if not isinstance(policy, dict):
            errors.append(f"{provider}: provider policy is not an object")
            continue
        creation = policy.get("creation")
        if creation not in {"enabled", "blockedPendingLab"}:
            errors.append(f"{provider}: unsupported creation policy")
            continue
        if provider == "fixed":
            if creation != "enabled" or policy.get("coordinationStrength") != "strong":
                errors.append("fixed provider must be enabled with strong coordination")
            continue
        evidence_id = policy.get("requiredEvidence")
        if not isinstance(evidence_id, str) or not evidence_id:
            errors.append(f"{provider}: requiredEvidence is missing")
            continue
        if creation == "blockedPendingLab":
            continue
        evidence_path = evidence_root / f"{evidence_id}.json"
        if not evidence_path.is_file():
            errors.append(f"{provider}: enabled without {evidence_id} evidence")
            continue
        try:
            payload = json.loads(evidence_path.read_text(encoding="utf-8"))
            head = head or _head(root)
            errors.extend(
                _validate_evidence(
                    payload,
                    evidence_id=evidence_id,
                    provider=provider,
                    head=head,
                    now=current,
                    evidence_root=evidence_root,
                )
            )
        except (OSError, json.JSONDecodeError, subprocess.SubprocessError) as exc:
            errors.append(f"{provider}: cannot validate {evidence_id}: {exc}")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=PROJECT_ROOT)
    args = parser.parse_args(argv)
    errors = check(args.root)
    if errors:
        print("[FAIL] provider evidence:")
        for error in errors:
            print(f"  - {error}")
        return 1
    print("[OK] provider evidence policy")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
