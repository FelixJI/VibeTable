"""Final-tree guard for every recorded migration handoff."""

import pytest

from qa.handoff import HANDOFFS_DIR, load_dependencies, previous_stage, verify_stage

SEQUENCE = load_dependencies()["sequence"]


def _stages_with_recorded_predecessor() -> list[str]:
    """Stages whose predecessor has a recorded handoff document on disk.

    G0-G5 are in the approved sequence but their predecessors are not yet
    recorded (the stages have not been implemented). We only verify stages
    whose predecessor handoff actually exists, so the guard stays green
    while G-stage work proceeds incrementally.
    """
    result: list[str] = []
    for stage in SEQUENCE[1:]:
        prev = previous_stage(stage, load_dependencies())
        if prev is not None and (HANDOFFS_DIR / f"{prev}.json").is_file():
            result.append(stage)
    return result


STAGES = _stages_with_recorded_predecessor()


@pytest.mark.parametrize("stage", STAGES)
def test_recorded_predecessor_handoff_matches_current_tree(stage: str) -> None:
    ok, reason = verify_stage(stage)
    assert ok, reason


# ---------------------------------------------------------------------------
# Protocol v2 optional fields (G0+ stages)
# ---------------------------------------------------------------------------


def test_supersedes_declarations_target_real_capabilities() -> None:
    """Every ``supersedes`` entry must reference a capability that the
    target stage actually declares in the manifest. Catches stale
    replacement declarations after a capability is removed."""
    deps = load_dependencies()
    all_caps = deps["capabilities"]
    for stage, metadata in deps.get("stageMetadata", {}).items():
        for entry in metadata.get("supersedes") or []:
            target_stage = entry.get("stage", "")
            target_cap = entry.get("capability", "")
            assert target_cap in all_caps.get(target_stage, []), (
                f"stage {stage!r} supersedes {target_cap!r} on stage "
                f"{target_stage!r}, but that capability is not declared"
            )


def test_g_stage_feature_flags_are_off_by_default() -> None:
    """Each G-stage must ship its feature flag in the OFF position until
    the stage's handoff gate passes (see implementation plan §3.1)."""
    deps = load_dependencies()
    # The primary capability-replacement switch for each stage.
    flag_defaults = {
        "G1": "fullFieldHistory",
        "G2": "localWorkspace",
        "G3": "workspaceIndex",
        "G4": "documentSchemes",
        "G5": "richDocumentDiff",
    }
    for stage, flag_name in flag_defaults.items():
        metadata = deps.get("stageMetadata", {}).get(stage, {})
        flags = metadata.get("featureFlags", {})
        assert flags.get(flag_name) is False, (
            f"stage {stage!r} flag {flag_name!r} must default to False "
            f"until the handoff gate passes"
        )


def test_legacy_handoffs_have_no_v2_fields() -> None:
    """A-E3 handoffs must remain v1-readable: they must not carry
    ``supersedes``, ``schemaSnapshotSha256`` or ``extensionHashes``."""
    import json

    deps = load_dependencies()
    legacy_stages = deps["sequence"][: deps["sequence"].index("G0")]
    for stage in legacy_stages:
        doc_path = HANDOFFS_DIR / f"{stage}.json"
        if not doc_path.is_file():
            continue
        doc = json.loads(doc_path.read_text(encoding="utf-8"))
        assert "supersedes" not in doc, f"legacy {stage!r} handoff must not carry supersedes"
        assert "schemaSnapshotSha256" not in doc, (
            f"legacy {stage!r} handoff must not carry schemaSnapshotSha256"
        )
        assert "extensionHashes" not in doc, (
            f"legacy {stage!r} handoff must not carry extensionHashes"
        )
