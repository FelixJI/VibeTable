from __future__ import annotations

from typing import Any

import pytest
from pydantic import ValidationError

from backend.adapters.pocketbase.backup import PocketBaseBackupService
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.contracts.backup import (
    CreateBackupParams,
    ListBackupsParams,
    RestoreBackupParams,
)


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        json_body: Any | None = None,
        headers: dict[str, str] | None = None,
        expected_status: tuple[int, ...] = (200,),
    ) -> Any:
        self.requests.append(
            {
                "method": method,
                "path": path,
                "query": query,
                "json_body": json_body,
                "headers": headers,
                "expected_status": expected_status,
            }
        )
        return self.responses.pop(0)


def entry(name: str = "manual_20260724_101500.zip") -> dict[str, Any]:
    return {
        "name": name,
        "size": 8192,
        "modified": "2026-07-24T10:15:00Z",
        "sha256": "a" * 64,
    }


@pytest.mark.asyncio
async def test_list_uses_only_the_fixed_backup_route_and_session_header() -> None:
    transport = FakeTransport([{"backups": [entry()]}])
    service = PocketBaseBackupService(
        PocketBaseClient(transport=transport, session_secret="s" * 64)
    )

    result = await service.list_backups(ListBackupsParams())

    assert result.backups[0].name == "manual_20260724_101500.zip"
    assert transport.requests == [
        {
            "method": "GET",
            "path": "/api/vibetable/v1/backups",
            "query": None,
            "json_body": None,
            "headers": {"X-VibeTable-Session": "s" * 64},
            "expected_status": (200,),
        }
    ]


@pytest.mark.asyncio
async def test_create_projects_integrity_without_exposing_attachment_details() -> None:
    transport = FakeTransport(
        [
            {
                "backup": entry(),
                "integrity": {
                    "checkedMetadata": 3,
                    "checkedVersions": 1,
                    "missingFiles": [],
                    "missingMetadata": [],
                    "corruptFiles": [],
                    "orphanFiles": [],
                    "orphanVersions": [],
                    "valid": True,
                },
            }
        ]
    )
    service = PocketBaseBackupService(
        PocketBaseClient(transport=transport, session_secret="t" * 64)
    )

    result = await service.create_backup(
        CreateBackupParams(name="manual_20260724_101500.zip")
    )

    assert result.backup.name == "manual_20260724_101500.zip"
    assert result.integrity_valid is True
    assert "missing_files" not in result.model_dump()
    assert transport.requests[0] == {
        "method": "POST",
        "path": "/api/vibetable/v1/backups",
        "query": None,
        "json_body": {"name": "manual_20260724_101500.zip"},
        "headers": {"X-VibeTable-Session": "t" * 64},
        "expected_status": (201,),
    }


@pytest.mark.asyncio
async def test_restore_requires_acknowledgement_but_sends_only_the_archive_name() -> None:
    transport = FakeTransport([{"status": "restarting"}])
    service = PocketBaseBackupService(
        PocketBaseClient(transport=transport, session_secret="u" * 64)
    )

    result = await service.restore_backup(
        RestoreBackupParams(
            name="manual_20260724_101500.zip",
            confirmed=True,
        )
    )

    assert result.status == "restarting"
    assert transport.requests[0] == {
        "method": "POST",
        "path": "/api/vibetable/v1/backups/restore",
        "query": None,
        "json_body": {"name": "manual_20260724_101500.zip"},
        "headers": {"X-VibeTable-Session": "u" * 64},
        "expected_status": (202,),
    }


@pytest.mark.parametrize(
    ("model", "payload"),
    [
        (CreateBackupParams, {"name": "../data.db.zip"}),
        (CreateBackupParams, {"name": "UPPER.zip"}),
        (CreateBackupParams, {"name": "safe.zip", "path": "C:/private"}),
        (RestoreBackupParams, {"name": "safe.zip", "confirmed": False}),
        (RestoreBackupParams, {"name": "safe.zip", "confirmed": True, "url": "http://x"}),
        (ListBackupsParams, {"sessionSecret": "nope"}),
    ],
)
def test_contracts_reject_paths_secrets_unknown_fields_and_unconfirmed_restore(
    model: type[Any],
    payload: dict[str, Any],
) -> None:
    with pytest.raises(ValidationError):
        model.model_validate(payload)


@pytest.mark.asyncio
async def test_invalid_sidecar_shapes_fail_closed() -> None:
    transport = FakeTransport([{"backups": [{"name": "../../escape.zip"}]}])
    service = PocketBaseBackupService(
        PocketBaseClient(transport=transport, session_secret="v" * 64)
    )

    with pytest.raises(ValidationError):
        await service.list_backups(ListBackupsParams())
