"""Closed backup adapter over the PocketBase product client."""

from __future__ import annotations

from typing import Any, Protocol

from pydantic import BaseModel, ConfigDict
from pydantic.alias_generators import to_camel

from backend.contracts.backup import (
    BackupCreateResult,
    BackupEntry,
    BackupListResult,
    BackupRestoreResult,
    CreateBackupParams,
    ListBackupsParams,
    RestoreBackupParams,
)


class BackupClient(Protocol):
    async def list_backups(self) -> dict[str, Any]: ...

    async def create_backup(self, name: str) -> dict[str, Any]: ...

    async def restore_backup(self, name: str) -> dict[str, Any]: ...


class _SidecarModel(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class _IntegrityReport(_SidecarModel):
    checked_metadata: int
    checked_versions: int
    missing_files: list[dict[str, Any]]
    missing_metadata: list[dict[str, Any]]
    corrupt_files: list[dict[str, Any]]
    orphan_files: list[dict[str, Any]]
    orphan_versions: list[dict[str, Any]]
    valid: bool


class _CreateResponse(_SidecarModel):
    backup: BackupEntry
    integrity: _IntegrityReport


class PocketBaseBackupService:
    """Validates backup responses and exposes only product-safe result shapes."""

    def __init__(self, client: BackupClient) -> None:
        self._client = client

    async def list_backups(self, _params: ListBackupsParams) -> BackupListResult:
        return BackupListResult.model_validate(await self._client.list_backups())

    async def create_backup(self, params: CreateBackupParams) -> BackupCreateResult:
        response = _CreateResponse.model_validate(await self._client.create_backup(params.name))
        if not response.integrity.valid:
            raise ValueError("PocketBase returned an invalid backup integrity result")
        return BackupCreateResult(
            backup=response.backup,
            integrity_valid=True,
        )

    async def restore_backup(self, params: RestoreBackupParams) -> BackupRestoreResult:
        # `confirmed` is a product-boundary acknowledgement. The fixed sidecar
        # endpoint accepts only the validated archive name.
        return BackupRestoreResult.model_validate(await self._client.restore_backup(params.name))


__all__ = ["BackupClient", "PocketBaseBackupService"]
