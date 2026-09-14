"""The Dashboard oracle replays a reachable Python producer, independent of Go."""

import copy
import json

import pytest

from contracts.v2 import generate_dashboard_python_oracle as oracle


def test_dashboard_oracle_replays_fixed_python_producer():
    captured = oracle.replay_producer()
    assert len(captured["cases"]) == 49
    assert len(captured["methods"]) == 7
    oracle.verify_capture(captured)


@pytest.mark.parametrize("value", [False, 0, "tampered"])
def test_dashboard_oracle_rejects_changed_projection(value):
    captured = json.loads(oracle.ORACLE.read_text(encoding="utf-8"))
    altered = copy.deepcopy(captured)
    altered["cases"][0]["response"]["result"]["dashboards"][0]["name"] = value
    with pytest.raises(ValueError, match="differs"):
        oracle.verify_capture(altered)
