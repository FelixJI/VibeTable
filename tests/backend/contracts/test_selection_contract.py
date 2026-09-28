"""Selection snapshot contract regressions for the optional formula-clock period."""

from __future__ import annotations

from backend.contracts.selection import QuerySnapshot


def _ordinary_snapshot() -> dict[str, object]:
    return {
        "snapshotId": "0" * 32,
        "digest": "a" * 64,
        "databaseId": "local",
        "table": "orders",
        "schemaRevision": "schema_0001",
        "dataRevision": 1,
        "normalizedQuery": {"offset": 0, "limit": 100},
    }


def test_ordinary_wire_stays_compatible_and_omits_the_clock_period() -> None:
    snapshot = QuerySnapshot.model_validate(_ordinary_snapshot())

    assert snapshot.clock_period is None
    wire = snapshot.model_dump(mode="json", by_alias=True, exclude_none=True)
    assert "clockPeriod" not in wire


def test_volatile_wire_carries_the_readable_clock_period() -> None:
    payload = _ordinary_snapshot() | {"clockPeriod": "2026-12-01T00:00:00Z"}

    snapshot = QuerySnapshot.model_validate(payload)

    assert snapshot.clock_period == "2026-12-01T00:00:00Z"
    wire = snapshot.model_dump(mode="json", by_alias=True, exclude_none=True)
    assert wire["clockPeriod"] == "2026-12-01T00:00:00Z"
