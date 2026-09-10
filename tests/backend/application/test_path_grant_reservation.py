from __future__ import annotations

import asyncio
from pathlib import Path

import pytest

from backend.application.path_grant import PathGrantError, SessionPathGrantStore


def test_expired_grant_cannot_start_a_reserved_import(tmp_path: Path) -> None:
    clock = [1000.0]
    store = SessionPathGrantStore(clock=lambda: clock[0], ttl_seconds=60)
    grant = store.issue(purpose="import_source", direction="read", path=str(tmp_path / "in.csv"))
    clock[0] += 61
    with (
        pytest.raises(PathGrantError) as error,
        store.reserve(grant.grant_id, purpose="import_source", direction="read"),
    ):
        pytest.fail("expired grant admitted an import")
    assert error.value.code == "grant_expired"


def test_admitted_import_can_commit_after_expiry_without_reopening_grant(tmp_path: Path) -> None:
    clock = [1000.0]
    store = SessionPathGrantStore(clock=lambda: clock[0], ttl_seconds=60)
    grant = store.issue(purpose="import_source", direction="read", path=str(tmp_path / "in.csv"))
    with store.reserve(grant.grant_id, purpose="import_source", direction="read") as commit:
        clock[0] += 61
        with pytest.raises(PathGrantError) as in_use:
            store.resolve(grant.grant_id, purpose="import_source", direction="read")
        assert in_use.value.code == "grant_in_use"
        commit()
    with pytest.raises(PathGrantError) as expired:
        store.resolve(grant.grant_id, purpose="import_source", direction="read")
    assert expired.value.code == "grant_expired"
    with pytest.raises(RuntimeError, match="no longer active"):
        commit()


@pytest.mark.parametrize("failure", [RuntimeError, asyncio.CancelledError])
def test_failed_or_cancelled_import_releases_reservation_without_consuming(
    tmp_path: Path, failure: type[BaseException]
) -> None:
    store = SessionPathGrantStore()
    path = tmp_path / "in.csv"
    grant = store.issue(purpose="import_source", direction="read", path=str(path))
    abandoned = None

    def fail_during_import() -> None:
        nonlocal abandoned
        with store.reserve(grant.grant_id, purpose="import_source", direction="read") as commit:
            abandoned = commit
            with (
                pytest.raises(PathGrantError) as concurrent,
                store.reserve(grant.grant_id, purpose="import_source", direction="read"),
            ):
                pytest.fail("a grant was admitted twice")
            assert concurrent.value.code == "grant_in_use"
            raise failure()

    with pytest.raises(failure):
        fail_during_import()
    assert abandoned is not None
    assert store.resolve(grant.grant_id, purpose="import_source", direction="read") == str(
        path.resolve()
    )
    with pytest.raises(RuntimeError, match="no longer active"):
        abandoned()
    with store.reserve(grant.grant_id, purpose="import_source", direction="read") as commit:
        commit()
    with pytest.raises(PathGrantError) as consumed:
        store.resolve(grant.grant_id, purpose="import_source", direction="read")
    assert consumed.value.code == "grant_consumed"
