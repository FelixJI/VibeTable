#!/usr/bin/env python3
"""Record and verify PocketBase migration handoffs.

Each handoff freezes four independently reviewable artifact groups:
the sidecar implementation identity, public product schema, ordered migration
manifest, and product capability declaration.  Hashes are derived from the
repository files declared in ``qa/handoff_dependencies.json``.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parent.parent
QA_DIR = REPO_ROOT / "qa"
HANDOFFS_DIR = REPO_ROOT / "docs" / "handoffs"
DEPENDENCIES_PATH = QA_DIR / "handoff_dependencies.json"
GATE_SUMMARY_PATH = REPO_ROOT / ".qa-next-summary.json"
DEFAULT_GATE_MAX_AGE = timedelta(hours=24)


def load_dependencies() -> dict[str, Any]:
    value = json.loads(DEPENDENCIES_PATH.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("handoff dependency manifest must be an object")
    return value


def previous_stage(stage: str, deps: dict[str, Any]) -> str | None:
    sequence = deps["sequence"]
    if stage not in sequence:
        raise ValueError(
            f"stage {stage!r} is not in the approved sequence "
            f"(known: {', '.join(sequence)})"
        )
    index = sequence.index(stage)
    return sequence[index - 1] if index else None


def git_head_sha(repo_root: Path = REPO_ROOT) -> str:
    process = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=repo_root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=30,
    )
    if process.returncode:
        raise RuntimeError("git rev-parse HEAD failed: " + (process.stderr or process.stdout))
    return process.stdout.strip()


def git_is_ancestor(
    maybe_ancestor: str,
    descendant: str,
    repo_root: Path = REPO_ROOT,
) -> bool:
    if maybe_ancestor == descendant:
        return True
    process = subprocess.run(
        ["git", "merge-base", "--is-ancestor", maybe_ancestor, descendant],
        cwd=repo_root,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    return process.returncode == 0


def sha256_of_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes().replace(b"\r\n", b"\n")).hexdigest()


def canonical_json_sha256(payload: object) -> str:
    encoded = json.dumps(
        payload,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def release_source_hash(
    deps: dict[str, Any],
    *,
    repo_root: Path | None = None,
) -> str:
    """Hash all release and gate source inputs, including untracked files."""

    repo_root = REPO_ROOT if repo_root is None else repo_root
    raw_inputs = deps.get("releaseIdentityInputs")
    raw_excluded = deps.get("releaseIdentityExcludedDirectories")
    raw_extensions = deps.get("releaseIdentityExtensions")
    if not isinstance(raw_inputs, list) or not raw_inputs:
        raise ValueError("releaseIdentityInputs must contain paths")
    if not isinstance(raw_excluded, list) or not all(
        isinstance(item, str) and item for item in raw_excluded
    ):
        raise ValueError("releaseIdentityExcludedDirectories must contain names")
    if not isinstance(raw_extensions, list) or not all(
        isinstance(item, str) and item.startswith(".") for item in raw_extensions
    ):
        raise ValueError("releaseIdentityExtensions must contain file extensions")
    excluded = {item.casefold() for item in raw_excluded}
    extensions = {item.casefold() for item in raw_extensions}
    files: set[Path] = set()
    for raw_path in raw_inputs:
        if not isinstance(raw_path, str) or not raw_path:
            raise ValueError("releaseIdentityInputs contains an invalid path")
        path = repo_root / raw_path
        if path.is_file():
            candidates = (path,)
        elif path.is_dir():
            candidates = (item for item in path.rglob("*") if item.is_file())
        else:
            raise FileNotFoundError(f"release identity input {raw_path!r} is missing")
        for candidate in candidates:
            relative = candidate.relative_to(repo_root)
            if any(part.casefold() in excluded for part in relative.parts):
                continue
            if candidate.suffix.casefold() not in extensions:
                continue
            files.add(candidate)
    if not files:
        raise ValueError("release identity resolved to zero source files")
    identities = {
        path.relative_to(repo_root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(files)
    }
    return canonical_json_sha256(identities)


def artifact_hashes(
    deps: dict[str, Any],
    *,
    repo_root: Path | None = None,
) -> dict[str, str]:
    """Hash every declared artifact group, failing on missing files."""

    repo_root = REPO_ROOT if repo_root is None else repo_root
    result: dict[str, str] = {}
    groups = deps.get("artifactFiles")
    if not isinstance(groups, dict) or not groups:
        raise ValueError("artifactFiles must declare at least one artifact group")
    required = {"sidecar", "schema", "migrations", "capabilities"}
    if set(groups) != required:
        raise ValueError(
            "artifactFiles groups must be exactly: "
            + ", ".join(sorted(required))
        )
    patterns = deps.get("artifactPatterns", {})
    if not isinstance(patterns, dict) or not set(patterns).issubset(groups):
        raise ValueError("artifactPatterns must contain only declared artifact groups")
    for group, raw_paths in groups.items():
        if not isinstance(raw_paths, list) or not raw_paths:
            raise ValueError(f"artifact group {group!r} must contain files")
        file_hashes: dict[str, str] = {}
        for raw_path in raw_paths:
            if not isinstance(raw_path, str) or not raw_path:
                raise ValueError(f"artifact group {group!r} contains an invalid path")
            path = repo_root / raw_path
            if not path.is_file():
                raise FileNotFoundError(f"artifact {raw_path!r} is missing")
            file_hashes[raw_path.replace("\\", "/")] = sha256_of_file(path)
        raw_patterns = patterns.get(group, [])
        if not isinstance(raw_patterns, list):
            raise ValueError(f"artifact patterns for {group!r} must be a list")
        for raw_pattern in raw_patterns:
            if not isinstance(raw_pattern, str) or not raw_pattern:
                raise ValueError(f"artifact group {group!r} contains an invalid pattern")
            matches = sorted(path for path in repo_root.glob(raw_pattern) if path.is_file())
            if not matches:
                raise FileNotFoundError(
                    f"artifact pattern {raw_pattern!r} did not match any files"
                )
            for path in matches:
                relative = path.relative_to(repo_root).as_posix()
                file_hashes[relative] = sha256_of_file(path)
        result[group] = canonical_json_sha256(file_hashes)
    return result


def _gate_summary() -> dict[str, Any] | None:
    if not GATE_SUMMARY_PATH.is_file():
        return None
    try:
        value = json.loads(GATE_SUMMARY_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def _parse_utc(value: object) -> datetime | None:
    if not isinstance(value, str) or not value:
        return None
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(UTC)


def validate_gate_summary(
    summary: object,
    *,
    commit: str,
    hashes: dict[str, str],
    source_hash: str | None = None,
    max_age: timedelta = DEFAULT_GATE_MAX_AGE,
    now: datetime | None = None,
    required_stages: list[str] | None = None,
) -> tuple[bool, str]:
    """Validate a release CI summary against the exact handoff identity."""

    if not isinstance(summary, dict):
        return False, "release gate summary is missing or unreadable"
    if summary.get("ok") is not True or summary.get("releaseEligible") is not True:
        return False, "release gate summary is not a successful full CI run"
    if summary.get("commit") != commit:
        return False, "release gate summary is not bound to the current commit"
    if summary.get("artifactHashes") != hashes:
        return False, "release gate summary artifact hashes do not match current artifacts"
    if source_hash is not None and summary.get("sourceHash") != source_hash:
        return False, "release gate summary source hash does not match current sources"
    generated_at = _parse_utc(summary.get("generatedAt"))
    if generated_at is None:
        return False, "release gate summary timestamp is missing or invalid"
    current = (now or datetime.now(UTC)).astimezone(UTC)
    age = current - generated_at
    if age < -timedelta(minutes=5):
        return False, "release gate summary timestamp is in the future"
    if age > max_age:
        return False, "release gate summary is stale"
    results = summary.get("results")
    if not isinstance(results, list) or not results:
        return False, "release gate summary contains no stage results"
    if any(
        not isinstance(result, dict) or result.get("returncode") != 0
        for result in results
    ):
        return False, "release gate summary contains a failed or malformed stage"
    if required_stages is not None:
        observed_stages = [result.get("stage") for result in results]
        if observed_stages != required_stages:
            return False, "release gate summary does not contain the exact CI stage sequence"
    return True, "release gate summary verified"


def record_stage(stage: str, *, run_gate: bool = True) -> int:
    """Write one handoff document for the current repository state."""

    deps = load_dependencies()
    if stage not in deps["sequence"]:
        print(f"error: unknown stage {stage!r}", file=sys.stderr)
        return 2
    try:
        hashes = artifact_hashes(deps)
        source_hash = release_source_hash(deps)
    except (ValueError, FileNotFoundError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3
    fixtures: dict[str, str] = {}
    for relative in deps.get("fixtures", {}).get(stage, []):
        path = REPO_ROOT / relative
        if not path.is_file():
            print(f"error: fixture {relative!r} is missing", file=sys.stderr)
            return 3
        fixtures[relative] = sha256_of_file(path)
    commit = git_head_sha()
    gate_summary = _gate_summary() if run_gate else None
    release_eligible = False
    if run_gate:
        max_age = timedelta(
            seconds=int(
                deps.get(
                    "gateSummaryMaxAgeSeconds",
                    int(DEFAULT_GATE_MAX_AGE.total_seconds()),
                )
            )
        )
        valid, reason = validate_gate_summary(
            gate_summary,
            commit=commit,
            hashes=hashes,
            source_hash=source_hash,
            max_age=max_age,
            required_stages=deps.get("requiredGateStages"),
        )
        if not valid:
            print(f"error: {reason}", file=sys.stderr)
            return 4
        release_eligible = True
    document = {
        "stage": stage,
        "recordedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "commit": commit,
        "protocolVersion": deps["protocolVersion"],
        "capabilities": deps["capabilities"].get(stage, []),
        "artifactHashes": hashes,
        "sourceHash": source_hash,
        "fixtures": fixtures,
        "releaseEligible": release_eligible,
        "gateSummary": gate_summary,
    }
    HANDOFFS_DIR.mkdir(parents=True, exist_ok=True)
    output = HANDOFFS_DIR / f"{stage}.json"
    output.write_text(
        json.dumps(document, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(f"recorded {stage} handoff -> {output.relative_to(REPO_ROOT)}")
    for group, digest in hashes.items():
        print(f"  {group}: {digest}")
    return 0


def verify_stage(stage: str) -> tuple[bool, str]:
    """Verify the predecessor handoff against current artifacts."""

    deps = load_dependencies()
    predecessor = previous_stage(stage, deps)
    if predecessor is None:
        return True, f"stage {stage!r} has no predecessor"
    path = HANDOFFS_DIR / f"{predecessor}.json"
    if not path.is_file():
        return False, f"predecessor {predecessor!r} handoff is missing"
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return False, f"predecessor {predecessor!r} handoff is unreadable: {exc}"
    if document.get("protocolVersion") != deps["protocolVersion"]:
        return False, "handoff protocol version mismatch"
    if document.get("releaseEligible") is not True:
        return False, "predecessor handoff is marked non-release-eligible"
    commit = document.get("commit")
    head = git_head_sha()
    if not isinstance(commit, str) or not commit:
        return False, "handoff commit is missing"
    if not git_is_ancestor(commit, head):
        return False, f"handoff commit {commit[:8]} is not an ancestor of {head[:8]}"
    required = set(deps["capabilities"].get(predecessor, []))
    recorded = set(document.get("capabilities") or [])
    if missing := sorted(required - recorded):
        return False, "handoff capabilities are missing: " + ", ".join(missing)
    try:
        expected_hashes = artifact_hashes(deps)
        expected_source_hash = release_source_hash(deps)
    except (ValueError, FileNotFoundError) as exc:
        return False, str(exc)
    if document.get("artifactHashes") != expected_hashes:
        return False, "sidecar/schema/migration/capability hashes changed"
    if document.get("sourceHash") != expected_source_hash:
        return False, "release or gate source inputs changed"
    expected_fixture_keys = set(deps.get("fixtures", {}).get(predecessor, []))
    fixtures = document.get("fixtures")
    if not isinstance(fixtures, dict) or set(fixtures) != expected_fixture_keys:
        return False, "handoff fixture keys do not exactly match the dependency manifest"
    for relative, recorded_hash in fixtures.items():
        fixture = REPO_ROOT / relative
        if not fixture.is_file() or sha256_of_file(fixture) != recorded_hash:
            return False, f"fixture {relative!r} is missing or changed"
    max_age = timedelta(
        seconds=int(
            deps.get(
                "gateSummaryMaxAgeSeconds",
                int(DEFAULT_GATE_MAX_AGE.total_seconds()),
            )
        )
    )
    valid, reason = validate_gate_summary(
        document.get("gateSummary"),
        commit=commit,
        hashes=expected_hashes,
        source_hash=expected_source_hash,
        max_age=max_age,
        required_stages=deps.get("requiredGateStages"),
    )
    if not valid:
        return False, reason
    return True, f"predecessor {predecessor!r} handoff verified at {head[:8]}"


def build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="handoff.py",
        description="Record and verify PocketBase product migration handoffs.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    record = commands.add_parser("record")
    record.add_argument("stage")
    record.add_argument("--no-gate", action="store_true")
    verify = commands.add_parser("verify")
    verify.add_argument("stage")
    commands.add_parser("list")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_arg_parser().parse_args(argv)
    if args.command == "record":
        return record_stage(args.stage, run_gate=not args.no_gate)
    if args.command == "verify":
        ok, reason = verify_stage(args.stage)
        print(("OK: " if ok else "FAIL: ") + reason)
        return 0 if ok else 1
    for stage in load_dependencies()["sequence"]:
        print(stage)
    return 0


if __name__ == "__main__":
    sys.exit(main())
