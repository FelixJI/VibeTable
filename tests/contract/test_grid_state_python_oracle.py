"""Replay grid presentation semantics from the fixed historical Python owner."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from contracts.v2.generate_grid_state_python_oracle import replay_producer, verify_capture


def test_historical_grid_state_replay_is_complete_and_detects_result_changes(
    tmp_path: Path,
) -> None:
    captured = replay_producer()
    verify_capture(captured)
    changed = json.loads(json.dumps(captured))
    changed["cases"][4]["result"]["conflict"] = False
    destination = tmp_path / "changed-oracle.json"
    destination.write_text(json.dumps(changed), encoding="utf-8")
    with pytest.raises(ValueError, match="differs from the fixed Python producer replay"):
        verify_capture(captured, destination)
