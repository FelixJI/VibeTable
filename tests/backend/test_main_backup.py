from __future__ import annotations

from typing import Any

import pytest

from backend.__main__ import _register_backup_methods
from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.contracts.backup import (
    BackupCreateResult,
    BackupEntry,
    BackupListResult,
    BackupRestoreResult,
)
from backend.rpc.dispatcher import CODE_INVALID_PARAMS, CODE_PRODUCT_DATA, RpcDispatcher


def backup_entry() -> BackupEntry:
    return BackupEntry(
        name="manual_20260724_101500.zip",
        size=12,
        modified="2026-07-24T10:15:00Z",
        sha256="b" * 64,
    )


class FakeBackupService:
    async def list_backups(self, _params: Any) -> BackupListResult:
        return BackupListResult(backups=[backup_entry()])

    async def create_backup(self, _params: Any) -> BackupCreateResult:
        return BackupCreateResult(backup=backup_entry(), integrity_valid=True)

    async def restore_backup(self, _params: Any) -> BackupRestoreResult:
        return BackupRestoreResult(status="restarting")


@pytest.mark.asyncio
async def test_registers_three_closed_backup_methods_and_serializes_aliases() -> None:
    dispatcher = RpcDispatcher()
    _register_backup_methods(dispatcher, FakeBackupService())

    assert {"backup.list", "backup.create", "backup.restore"} <= set(dispatcher.registered_methods)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "backup-1",
            "method": "backup.create",
            "params": {"name": "manual_20260724_101500.zip"},
        }
    )

    assert response is not None
    assert response["result"]["integrityValid"] is True
    assert response["result"]["backup"]["sha256"] == "b" * 64


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("method", "params"),
    [
        ("backup.list", {"url": "http://127.0.0.1"}),
        ("backup.create", {"name": "../unsafe.zip"}),
        (
            "backup.restore",
            {"name": "manual_20260724_101500.zip", "confirmed": False},
        ),
        (
            "backup.restore",
            {
                "name": "manual_20260724_101500.zip",
                "confirmed": True,
                "path": "C:/private/data.db",
            },
        ),
    ],
)
async def test_dispatcher_rejects_non_closed_backup_payloads(
    method: str,
    params: dict[str, Any],
) -> None:
    dispatcher = RpcDispatcher()
    _register_backup_methods(dispatcher, FakeBackupService())

    response = await dispatcher.dispatch(
        {"jsonrpc": "2.0", "id": "bad", "method": method, "params": params}
    )

    assert response is not None
    assert response["error"]["code"] == CODE_INVALID_PARAMS


class ErrorBackupService(FakeBackupService):
    async def create_backup(self, _params: Any) -> BackupCreateResult:
        raise PocketBaseProductError(
            status=500,
            payload={
                "code": "backup.create_failed",
                "message": "application backup could not be created",
                "retryable": True,
            },
        )


@pytest.mark.asyncio
async def test_backup_sidecar_errors_use_sanitized_product_error_mapping() -> None:
    dispatcher = RpcDispatcher()
    _register_backup_methods(dispatcher, ErrorBackupService())

    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "failed",
            "method": "backup.create",
            "params": {"name": "manual_20260724_101500.zip"},
        }
    )

    assert response is not None
    assert response["error"]["code"] == CODE_PRODUCT_DATA
    assert response["error"]["data"] == {
        "kind": "product_data_error",
        "message": "application backup could not be created",
        "code": "backup.create_failed",
        "path": None,
        "details": {},
        "retryable": True,
    }
