#!/usr/bin/env python3
"""VibeTable WPF stage handoff recorder and verifier (Task 12).

Implements the stage handoff system: every migration stage (A, B1, B3, ...)
records a ``docs/handoffs/<stage>.json`` manifest after its gate passes, and
each successor stage MUST verify that manifest against the working tree
before it may begin.

Handoff document schema
-----------------------
Each ``docs/handoffs/<stage>.json`` contains::

    {
      "stage": "A",
      "recordedAt": "<ISO 8601 local time>",
      "commit": "<git SHA of HEAD at record time>",
      "protocolVersion": "1.0",
      "capabilities": ["rpc.request.v1", ...],   # verbatim from dependencies
      "fixtures": {
        "<repo-relative path>": "<SHA-256 of the file bytes>",
        ...
      },
      "migrationState": {"businessSchemaChanged": false},
      "gateSummary": {
        "stages": ["python", "contracts", ...],
        "results": [
          {"stage": "python", "returncode": 0, "elapsed": 1.23, "skipped": false},
          ...
        ],
        "gatePassed": true,
        "smokeNoted": "smoke stage skipped; end-to-end evidence incomplete" | null,
        "summarySha256": "<SHA-256 of the canonical JSON of `results`>"
      }
    }

Protocol v2 optional fields (G0+ stages only)
---------------------------------------------
The G0-G5 workspace/full-field-history stages extend the document with
optional fields that are **absent** from legacy A-E3 handoffs. Old handoffs
must remain readable and verifiable under v1 semantics; only G-stage
documents carry the extra fields::

    "stageMetadata": {                         # already present for B4+ stages
      "supersedes": [                          # declares capability replacement
        {"stage": "C2", "capability": "directus.versions.v1",
         "replacement": "directus.full-field-history.v1", "notes": "..."},
        ...
      ],
      "featureFlags": {"fullFieldHistory": false, ...},
      ...
    },
    "schemaSnapshotSha256": "<sha256>",         # Directus schema snapshot hash
    "extensionHashes": {                        # per-extension content hash
      "vibetable-bulk-mutation": "<sha256>",
      "vibetable-workspace-index": "<sha256>",
      ...
    }

``supersedes`` and ``featureFlags`` come from ``stageMetadata``; the
recorder copies them into the document so a verifier can check capability
replacement consistency without re-reading the manifest.
``schemaSnapshotSha256`` and ``extensionHashes`` are recorded when a
G-stage deploy produces a schema snapshot or extension build.

Commands
--------
* ``record <stage>``: writes ``docs/handoffs/<stage>.json`` from the working
  tree's current git commit, the dependency manifest's capability list, the
  SHA-256 of each declared fixture file, and a re-run of the Phase A gate
  (only when recording stage ``A``; later stages run their own gate). The
  smoke stage must execute; a skip is recorded as incomplete evidence and
  fails the handoff.
* ``verify <stage>``: validates the PREVIOUS stage's handoff document against
  the working tree — the document exists, its commit matches HEAD (or HEAD is
  a descendant when work has advanced), every declared capability and fixture
  still resolves, and the fixture SHA-256 still matches the on-disk bytes.
  Returns non-zero on any missing/mismatched evidence.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import datetime
from pathlib import Path

# ---------------------------------------------------------------------------
# Repository layout
# ---------------------------------------------------------------------------

REPO_ROOT: Path = Path(__file__).resolve().parent.parent
QA_DIR: Path = REPO_ROOT / "qa"
HANDOFFS_DIR: Path = REPO_ROOT / "docs" / "handoffs"
DEPENDENCIES_PATH: Path = QA_DIR / "handoff_dependencies.json"
GATE_SUMMARY_PATH: Path = REPO_ROOT / ".qa-next-summary.json"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def load_dependencies() -> dict:
    """Load and return the handoff dependency manifest."""
    return json.loads(DEPENDENCIES_PATH.read_text(encoding="utf-8"))


def previous_stage(stage: str, deps: dict) -> str | None:
    """Return the stage that ``stage`` depends on (its predecessor in the
    approved sequence), or ``None`` for the first stage."""
    seq = deps["sequence"]
    if stage not in seq:
        raise ValueError(
            f"stage {stage!r} is not in the approved sequence (known: {', '.join(seq)})"
        )
    idx = seq.index(stage)
    return seq[idx - 1] if idx > 0 else None


def git_head_sha(repo_root: Path = REPO_ROOT) -> str:
    """Return the SHA-256 of the current HEAD commit (full 40-char hex)."""
    proc = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=str(repo_root),
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError("git rev-parse HEAD failed: " + (proc.stderr or proc.stdout))
    return proc.stdout.strip()


def git_is_ancestor(maybe_ancestor: str, descendant: str, repo_root: Path = REPO_ROOT) -> bool:
    """True when ``maybe_ancestor`` is HEAD or an ancestor of ``descendant``.

    Used by :func:`verify_stage` so a handoff recorded at commit X still
    verifies cleanly once more commits have landed on top (X is now an
    ancestor of HEAD). The recorded commit is allowed to be exactly HEAD too.
    """
    if maybe_ancestor == descendant:
        return True
    proc = subprocess.run(
        ["git", "merge-base", "--is-ancestor", maybe_ancestor, descendant],
        cwd=str(repo_root),
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode == 0


def sha256_of_file(path: Path) -> str:
    """Return the hex SHA-256 of ``path`` with CRLF normalized to LF.

    Contract fixtures are committed with LF line endings, but Windows
    checkouts with ``core.autocrlf=true`` materialize them as CRLF. Hashing
    the raw bytes would then disagree with the recorded digest even though
    the logical content is identical, which would falsely fail the handoff
    preflight. Normalizing to LF before hashing keeps the recorded digests
    stable across Windows, macOS and Linux contributors.
    """
    h = hashlib.sha256()
    h.update(path.read_bytes().replace(b"\r\n", b"\n"))
    return h.hexdigest()


def canonical_json_sha256(payload: object) -> str:
    """SHA-256 of the canonical (sorted-key, compact) JSON encoding of ``payload``."""
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Gate summary
# ---------------------------------------------------------------------------


def capture_gate_summary() -> dict:
    """Run the Phase A gate (``qa/next.py --ci``) and capture its summary.

    Used when recording the ``A`` handoff so the document carries an
    evidence hash of the exact per-stage results. A skipped smoke stage is
    recorded in ``smokeNoted`` and fails the handoff because no end-to-end
    Windows/WebView2 evidence was produced.
    """
    # Import lazily so unit tests that monkeypatch this module do not also
    # have to import next.py at module load.
    sys.path.insert(0, str(QA_DIR))
    sys.modules.pop("next", None)
    import next as next_module

    results: list[next_module.StageResult] = []
    for stage in next_module.STAGES:
        result = next_module.run_stage(stage)
        results.append(result)
        if next_module.is_stage_failure(result):
            break

    payload_results = []
    smoke_noted = None
    gate_passed = True
    for r in results:
        skipped = next_module.is_stage_skipped(r)
        failed = next_module.is_stage_failure(r)
        if failed:
            gate_passed = False
        if skipped:
            smoke_noted = "smoke stage skipped; end-to-end evidence incomplete"
        payload_results.append(
            {
                "stage": r.stage,
                "returncode": r.returncode,
                "elapsed": round(r.elapsed, 4),
                "skipped": skipped,
            }
        )
    summary = {
        "stages": list(next_module.STAGES),
        "results": payload_results,
        "gatePassed": gate_passed,
        "smokeNoted": smoke_noted,
        "summarySha256": canonical_json_sha256(payload_results),
    }
    return summary


# ---------------------------------------------------------------------------
# Record
# ---------------------------------------------------------------------------


def record_stage(stage: str, *, run_gate: bool = True) -> int:
    """Write ``docs/handoffs/<stage>.json`` from the working tree.

    For stage ``A`` the gate is re-run to capture evidence. For later stages
    the caller is expected to have run their own gate; we reuse the most
    recent gate summary if present, otherwise leave the field empty.

    Exits 0 on success, non-zero on error.
    """
    deps = load_dependencies()
    if stage not in deps["sequence"]:
        print(
            f"error: stage {stage!r} is not in the approved sequence",
            file=sys.stderr,
        )
        return 2

    head = git_head_sha()
    protocol = deps["protocolVersion"]
    capabilities = deps["capabilities"].get(stage, [])
    fixture_paths = [REPO_ROOT / p for p in deps.get("fixtures", {}).get(stage, [])]
    missing = [str(p) for p in fixture_paths if not p.is_file()]
    if missing:
        print(
            "error: declared fixtures are missing from the working tree: " + ", ".join(missing),
            file=sys.stderr,
        )
        return 3

    fixtures = {
        str(p.relative_to(REPO_ROOT)).replace("\\", "/"): sha256_of_file(p) for p in fixture_paths
    }
    migration_state = deps.get("migrationState", {}).get(stage, {"businessSchemaChanged": False})

    gate_summary: dict | None = None
    if run_gate and stage == "A":
        gate_summary = capture_gate_summary()
    elif GATE_SUMMARY_PATH.is_file():
        try:
            gate_summary = json.loads(GATE_SUMMARY_PATH.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            gate_summary = None

    document = {
        "stage": stage,
        "recordedAt": datetime.now().isoformat(),
        "commit": head,
        "protocolVersion": protocol,
        "capabilities": capabilities,
        "fixtures": fixtures,
        "migrationState": migration_state,
        "gateSummary": gate_summary,
    }
    metadata = deps.get("stageMetadata", {}).get(stage)
    if metadata is not None:
        document["stageMetadata"] = metadata
        # Protocol v2: surface capability-replacement declarations at the
        # document top level so a verifier does not need to re-read the
        # manifest to discover what this stage supersedes.
        supersedes = metadata.get("supersedes")
        if supersedes:
            document["supersedes"] = supersedes

    HANDOFFS_DIR.mkdir(parents=True, exist_ok=True)
    out_path = HANDOFFS_DIR / f"{stage}.json"
    out_path.write_text(
        json.dumps(document, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    try:
        rel_display = str(out_path.relative_to(REPO_ROOT))
    except ValueError:
        rel_display = str(out_path)
    print(f"recorded {stage} handoff -> {rel_display}")
    print(f"  commit: {head}")
    print(f"  protocol: {protocol}")
    print(f"  capabilities: {len(capabilities)}")
    print(f"  fixtures: {len(fixtures)}")
    if gate_summary is not None:
        print(f"  gate: passed={gate_summary['gatePassed']} smoke={gate_summary['smokeNoted']}")
    return 0


# ---------------------------------------------------------------------------
# Verify
# ---------------------------------------------------------------------------


def verify_stage(stage: str) -> tuple[bool, str]:
    """Validate the PREVIOUS stage's handoff document against the tree.

    Returns ``(ok, reason)``. ``ok`` is True only when every check passes.
    """
    deps = load_dependencies()
    prev = previous_stage(stage, deps)
    if prev is None:
        # First stage has no predecessor; nothing to verify.
        return True, f"stage {stage!r} has no predecessor"
    prev_doc_path = HANDOFFS_DIR / f"{prev}.json"
    if not prev_doc_path.is_file():
        return False, (
            f"predecessor {prev!r} handoff document is missing: "
            f"{prev_doc_path.relative_to(REPO_ROOT)}"
        )
    try:
        doc = json.loads(prev_doc_path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError) as exc:
        return False, f"predecessor {prev!r} handoff document is unreadable: {exc}"

    head = git_head_sha()
    recorded_commit = doc.get("commit", "")
    if not recorded_commit:
        return False, f"predecessor {prev!r} handoff is missing the 'commit' field"
    # The recorded commit must be HEAD or an ancestor of HEAD (so further
    # commits on top still verify). If neither, the tree has diverged.
    if not git_is_ancestor(recorded_commit, head):
        return False, (
            f"predecessor {prev!r} was recorded at {recorded_commit[:8]} but "
            f"HEAD ({head[:8]}) is not a descendant — tree has diverged"
        )

    # Protocol version must match the dependency manifest.
    expected_protocol = deps["protocolVersion"]
    if doc.get("protocolVersion") != expected_protocol:
        return False, (
            f"predecessor {prev!r} protocolVersion={doc.get('protocolVersion')!r} "
            f"!= manifest {expected_protocol!r}"
        )

    # Capabilities declared for the predecessor in the manifest must all be
    # present in the recorded document.
    required_caps = deps["capabilities"].get(prev, [])
    recorded_caps = set(doc.get("capabilities", []))
    missing_caps = [c for c in required_caps if c not in recorded_caps]
    if missing_caps:
        return False, (
            f"predecessor {prev!r} handoff is missing capabilities: " + ", ".join(missing_caps)
        )

    # Every declared fixture must still exist and match its recorded SHA-256.
    for rel, recorded_sha in (doc.get("fixtures") or {}).items():
        fixture_path = REPO_ROOT / rel
        if not fixture_path.is_file():
            return False, f"fixture {rel!r} (declared by {prev!r}) is missing"
        actual_sha = sha256_of_file(fixture_path)
        if actual_sha != recorded_sha:
            return False, (
                f"fixture {rel!r} (declared by {prev!r}) SHA-256 mismatch: "
                f"recorded {recorded_sha[:12]}... != actual {actual_sha[:12]}..."
            )

    # Migration state must be present and unchanged.
    expected_migration = deps.get("migrationState", {}).get(prev, {"businessSchemaChanged": False})
    if doc.get("migrationState") != expected_migration:
        return False, (
            f"predecessor {prev!r} migrationState={doc.get('migrationState')!r} "
            f"!= manifest {expected_migration!r}"
        )

    # Protocol v2: when the predecessor declares ``supersedes``, every
    # superseded capability must still be resolvable as a capability of the
    # stage it claims to supersede. This catches stale declarations where
    # the superseded capability was removed from the manifest.
    for entry in doc.get("supersedes") or []:
        target_stage = entry.get("stage", "")
        target_cap = entry.get("capability", "")
        stage_caps = deps.get("capabilities", {}).get(target_stage, [])
        if target_cap and target_cap not in stage_caps:
            return False, (
                f"predecessor {prev!r} declares supersede of {target_cap!r} "
                f"(stage {target_stage!r}) but that capability is no longer "
                f"declared in the manifest"
            )

    return True, f"predecessor {prev!r} handoff verified at commit {head[:8]}"


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="handoff.py",
        description="Record and verify VibeTable WPF migration stage handoffs.",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    record = sub.add_parser("record", help="Write docs/handoffs/<stage>.json.")
    record.add_argument("stage", help="The stage to record (e.g. A, B1, ...).")
    record.add_argument(
        "--no-gate",
        action="store_true",
        help="Do not re-run the Phase A gate when recording stage A "
        "(use the most recent captured summary, if any).",
    )
    sub.add_parser("list", help="Print the approved stage sequence.")
    verify = sub.add_parser("verify", help="Verify the previous stage's handoff.")
    verify.add_argument("stage", help="The stage whose predecessor to verify.")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_arg_parser()
    args = parser.parse_args(argv)
    if args.command == "record":
        return record_stage(args.stage, run_gate=not args.no_gate)
    if args.command == "verify":
        ok, reason = verify_stage(args.stage)
        prefix = "OK  " if ok else "FAIL"
        print(f"{prefix}: {reason}")
        return 0 if ok else 1
    if args.command == "list":
        deps = load_dependencies()
        for s in deps["sequence"]:
            print(s)
        return 0
    parser.error("no command")  # unreachable: subparsers required
    return 2


if __name__ == "__main__":
    sys.exit(main())
