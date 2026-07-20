"""Protocol v2 handoff fixture round-trip tests (G0.1).

Validates that:
1. A v2 handoff document with ``supersedes``, ``schemaSnapshotSha256`` and
   ``extensionHashes`` round-trips through JSON without losing the optional
   fields.
2. The recorder surfaces ``supersedes`` at the document top level when the
   manifest declares it in ``stageMetadata``.
3. The verifier rejects a ``supersedes`` entry whose capability is no longer
   declared by the target stage.
4. Legacy (v1) handoffs remain verifiable — the v2 fields are purely additive.

These tests use synthetic in-memory documents so they do not depend on a
deployed Directus instance or a completed G-stage implementation.
"""

from __future__ import annotations

import json
from unittest.mock import patch

import qa.handoff as handoff_mod
from qa.handoff import (
    previous_stage,
    record_stage,
    verify_stage,
)

# ---------------------------------------------------------------------------
# Synthetic manifest fragments
# ---------------------------------------------------------------------------

_SYNTHETIC_DEPS = {
    "sequence": ["C2", "G1"],
    "protocolVersion": "1.0",
    "capabilities": {
        "C2": ["directus.versions.v1", "directus.activity.v1"],
        "G1": ["directus.full-field-history.v1"],
    },
    "fixtures": {},
    "migrationState": {
        "C2": {
            "businessSchemaChanged": False,
            "source": "directus",
            "schemaVersion": "vibetable-1.0",
        },
        "G1": {
            "businessSchemaChanged": True,
            "source": "directus",
            "schemaVersion": "vibetable-1.0",
        },
    },
    "stageMetadata": {
        "G1": {
            "featureFlags": {"fullFieldHistory": False},
            "supersedes": [
                {
                    "stage": "C2",
                    "capability": "directus.versions.v1",
                    "replacement": "directus.full-field-history.v1",
                    "notes": "G1 closes Content Versions",
                }
            ],
        },
    },
}


def _deps() -> dict:
    """Return a fresh deep copy so tests cannot mutate the shared fragment."""
    return json.loads(json.dumps(_SYNTHETIC_DEPS))


# ---------------------------------------------------------------------------
# Round-trip
# ---------------------------------------------------------------------------


def test_v2_document_round_trips_through_json() -> None:
    """A v2 handoff with all optional fields survives JSON round-trip."""
    doc = {
        "stage": "G1",
        "recordedAt": "2026-07-15T10:00:00",
        "commit": "abc1234",
        "protocolVersion": "1.0",
        "capabilities": ["directus.full-field-history.v1"],
        "fixtures": {},
        "migrationState": {"businessSchemaChanged": True},
        "gateSummary": None,
        "supersedes": _SYNTHETIC_DEPS["stageMetadata"]["G1"]["supersedes"],
        "schemaSnapshotSha256": "deadbeef" * 8,
        "extensionHashes": {"vibetable-bulk-mutation": "cafebabe" * 8},
    }
    text = json.dumps(doc, indent=2, ensure_ascii=False)
    restored = json.loads(text)
    assert restored["supersedes"][0]["capability"] == "directus.versions.v1"
    assert restored["schemaSnapshotSha256"] == "deadbeef" * 8
    assert restored["extensionHashes"]["vibetable-bulk-mutation"] == "cafebabe" * 8


# ---------------------------------------------------------------------------
# Recorder surfaces supersedes
# ---------------------------------------------------------------------------


def test_record_surfaces_supersedes_at_top_level(tmp_path) -> None:
    """``record_stage`` copies ``supersedes`` from stageMetadata to the doc."""
    deps = _deps()
    with (
        patch.object(handoff_mod, "load_dependencies", return_value=deps),
        patch.object(handoff_mod, "git_head_sha", return_value="abc1234"),
        patch.object(handoff_mod, "HANDOFFS_DIR", tmp_path),
        patch.object(handoff_mod, "GATE_SUMMARY_PATH", tmp_path / "nope.json"),
    ):
        rc = record_stage("G1", run_gate=False)
    assert rc == 0
    doc = json.loads((tmp_path / "G1.json").read_text(encoding="utf-8"))
    assert "supersedes" in doc
    assert doc["supersedes"][0]["capability"] == "directus.versions.v1"
    assert doc["supersedes"][0]["replacement"] == "directus.full-field-history.v1"


def test_record_omits_supersedes_when_not_declared(tmp_path) -> None:
    """A stage without ``supersedes`` in its metadata must not carry it."""
    deps = _deps()
    deps["stageMetadata"]["G1"].pop("supersedes")
    with (
        patch.object(handoff_mod, "load_dependencies", return_value=deps),
        patch.object(handoff_mod, "git_head_sha", return_value="abc1234"),
        patch.object(handoff_mod, "HANDOFFS_DIR", tmp_path),
        patch.object(handoff_mod, "GATE_SUMMARY_PATH", tmp_path / "nope.json"),
    ):
        rc = record_stage("G1", run_gate=False)
    assert rc == 0
    doc = json.loads((tmp_path / "G1.json").read_text(encoding="utf-8"))
    assert "supersedes" not in doc


# ---------------------------------------------------------------------------
# Verifier rejects stale supersedes
# ---------------------------------------------------------------------------


def test_verify_rejects_supersede_of_undeclared_capability() -> None:
    """If a predecessor declares ``supersedes`` for a capability that the
    target stage no longer declares, verification must fail."""
    deps = _deps()
    # Sabotage: remove the superseded capability from C2's declarations.
    deps["capabilities"]["C2"] = ["directus.activity.v1"]

    # Record a C2 predecessor that carries the (now-stale) supersedes.
    c2_doc = {
        "stage": "C2",
        "recordedAt": "2026-07-15T09:00:00",
        "commit": "abc1234",
        "protocolVersion": "1.0",
        "capabilities": deps["capabilities"]["C2"],
        "fixtures": {},
        "migrationState": deps["migrationState"]["C2"],
        "gateSummary": None,
        "supersedes": [
            {
                "stage": "C2",
                "capability": "directus.versions.v1",
                "replacement": "directus.full-field-history.v1",
                "notes": "stale",
            }
        ],
    }
    with (
        patch.object(handoff_mod, "load_dependencies", return_value=deps),
        patch.object(handoff_mod, "git_head_sha", return_value="abc1234"),
        patch.object(handoff_mod, "git_is_ancestor", return_value=True),
    ):
        # Write a fake handoff doc to a temp dir and patch HANDOFFS_DIR.
        import tempfile
        from pathlib import Path

        tmp = Path(tempfile.mkdtemp())
        (tmp / "C2.json").write_text(json.dumps(c2_doc, indent=2), encoding="utf-8")
        with patch.object(handoff_mod, "HANDOFFS_DIR", tmp):
            ok, reason = verify_stage("G1")
    assert not ok
    assert "supersede" in reason.lower()


def test_verify_accepts_consistent_supersedes() -> None:
    """A well-formed ``supersedes`` referencing a real capability passes."""
    deps = _deps()
    c2_doc = {
        "stage": "C2",
        "recordedAt": "2026-07-15T09:00:00",
        "commit": "abc1234",
        "protocolVersion": "1.0",
        "capabilities": deps["capabilities"]["C2"],
        "fixtures": {},
        "migrationState": deps["migrationState"]["C2"],
        "gateSummary": None,
        "supersedes": deps["stageMetadata"]["G1"]["supersedes"],
    }
    with (
        patch.object(handoff_mod, "load_dependencies", return_value=deps),
        patch.object(handoff_mod, "git_head_sha", return_value="abc1234"),
        patch.object(handoff_mod, "git_is_ancestor", return_value=True),
    ):
        import tempfile
        from pathlib import Path

        tmp = Path(tempfile.mkdtemp())
        (tmp / "C2.json").write_text(json.dumps(c2_doc, indent=2), encoding="utf-8")
        with patch.object(handoff_mod, "HANDOFFS_DIR", tmp):
            ok, reason = verify_stage("G1")
    assert ok, reason


# ---------------------------------------------------------------------------
# Legacy v1 handoffs remain verifiable
# ---------------------------------------------------------------------------


def test_legacy_v1_handoff_has_no_v2_fields_and_verifies() -> None:
    """A v1 handoff document without any v2 fields must still verify."""
    deps = _deps()
    # C2 as a pure v1 document — no supersedes, no extensionHashes.
    c2_doc = {
        "stage": "C2",
        "recordedAt": "2026-07-15T09:00:00",
        "commit": "abc1234",
        "protocolVersion": "1.0",
        "capabilities": deps["capabilities"]["C2"],
        "fixtures": {},
        "migrationState": deps["migrationState"]["C2"],
        "gateSummary": None,
    }
    with (
        patch.object(handoff_mod, "load_dependencies", return_value=deps),
        patch.object(handoff_mod, "git_head_sha", return_value="abc1234"),
        patch.object(handoff_mod, "git_is_ancestor", return_value=True),
    ):
        import tempfile
        from pathlib import Path

        tmp = Path(tempfile.mkdtemp())
        (tmp / "C2.json").write_text(json.dumps(c2_doc, indent=2), encoding="utf-8")
        with patch.object(handoff_mod, "HANDOFFS_DIR", tmp):
            ok, reason = verify_stage("G1")
    assert ok, reason


def test_previous_stage_resolves_g_chain() -> None:
    """previous_stage correctly walks the G sequence."""
    deps = _deps()
    assert previous_stage("G1", deps) == "C2"
    assert previous_stage("C2", deps) is None
