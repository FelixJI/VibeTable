"""Closed product contracts for local PocketBase backups."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


class CamelModel(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


BackupName = Annotated[
    str,
    Field(
        min_length=5,
        max_length=67,
        pattern=r"^[a-z0-9][a-z0-9_-]{0,62}\.zip$",
    ),
]
Sha256 = Annotated[str, Field(pattern=r"^[0-9a-f]{64}$")]


class ListBackupsParams(CamelModel):
    """The list operation has no caller-controlled storage inputs."""


class CreateBackupParams(CamelModel):
    name: BackupName


class RestoreBackupParams(CamelModel):
    name: BackupName
    confirmed: Literal[True]


class BackupEntry(CamelModel):
    name: BackupName
    size: int = Field(ge=0)
    modified: datetime
    sha256: Sha256


class BackupListResult(CamelModel):
    backups: list[BackupEntry] = Field(default_factory=list)


class BackupCreateResult(CamelModel):
    backup: BackupEntry
    integrity_valid: Literal[True]


class BackupRestoreResult(CamelModel):
    status: Literal["restarting"]


__all__ = [
    "BackupCreateResult",
    "BackupEntry",
    "BackupListResult",
    "BackupName",
    "BackupRestoreResult",
    "CreateBackupParams",
    "ListBackupsParams",
    "RestoreBackupParams",
]
