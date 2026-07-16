"""E2 release manifest, Launcher/Updater/Installer and Directus compatibility contracts.

The release manifest binds the WPF host, Python backend, Web assets, protocol
fixtures and SBOM into one signed, versioned client release. It declares the
Directus API/capability/schema contract range so the Launcher can refuse to
start business operations against an incompatible server.

Key invariants:
* The manifest contains NO endpoint secret, token or production URL.
* The Updater runs in a separate process, downloads + verifies + unpacks to a
  new version directory, then atomically swaps the Launcher pointer.
* Rollback swaps the pointer back; it never touches Directus schema or data.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# Release manifest (E2 Task 1)
# ---------------------------------------------------------------------------


class ComponentHash(CamelModel):
    """A content hash for one release component."""

    component: str = Field(min_length=1, max_length=64)
    sha256: str = Field(min_length=64, max_length=64)


class DirectusCompatibilityRange(CamelModel):
    """The Directus API/capability/schema contract this release supports.

    The Launcher reads the server's capability report at startup and refuses
    business operations if the server falls outside this range.
    """

    api_range: str = Field(default=">=12 <13", max_length=64)
    required_capabilities: list[str] = Field(default_factory=list, max_length=64)
    schema_contract: str = Field(default="vibetable-1.0", max_length=64)
    dashboard_panel_manifest_version: str = Field(
        default="dashboard-panel-manifest.v1", max_length=64
    )
    asset_preset_keys: list[str] = Field(default_factory=list, max_length=32)


class ReleaseManifest(CamelModel):
    """The signed, versioned client release manifest.

    Wire form::

        {"releaseVersion": "1.0.0", "directusCompatibility": {...},
         "components": [...], "sbomPath": "sbom.json", "signature": "..."}

    ``signature`` covers the manifest + all component hashes. The manifest
    contains NO endpoint secret, token or production URL.
    """

    release_version: str = Field(min_length=1, max_length=32)
    directus_compatibility: DirectusCompatibilityRange
    components: list[ComponentHash] = Field(default_factory=list, max_length=64)
    sbom_path: str = Field(default="", max_length=256)
    signature: str = Field(default="", max_length=2048)
    built_at: str = Field(default="", max_length=64)


# ---------------------------------------------------------------------------
# Launcher pointer (E2 Task 3)
# ---------------------------------------------------------------------------


class LauncherPointer(CamelModel):
    """The stable Launcher's pointer to the active version directory.

    The Launcher reads this, verifies the version directory + manifest signature,
    then starts the client. The Updater atomically swaps this file.
    """

    active_version: str = Field(min_length=1, max_length=32)
    version_directory: str = Field(min_length=1, max_length=512)
    manifest_path: str = Field(default="", max_length=512)
    previous_version: str | None = Field(default=None, max_length=32)


# ---------------------------------------------------------------------------
# Updater (E2 Task 5)
# ---------------------------------------------------------------------------


UpdateState = Literal[
    "idle",
    "downloading",
    "verifying",
    "unpacking",
    "swapping",
    "succeeded",
    "rollback-required",
    "rolled-back",
    "failed",
]


class UpdateRequest(CamelModel):
    """A request to update to a new release version.

    The Updater waits for non-interruptible local tasks and remote mutation
    state verification before swapping the pointer.
    """

    target_version: str = Field(min_length=1, max_length=32)
    manifest_url: str = Field(default="", max_length=2048)
    force: bool = False


class UpdateResult(CamelModel):
    """Result of an update attempt."""

    target_version: str = Field(min_length=1, max_length=32)
    state: UpdateState
    previous_version: str | None = Field(default=None, max_length=32)
    error: str | None = Field(default=None, max_length=1024)


class RollbackResult(CamelModel):
    """Result of a pointer rollback."""

    rolled_back_to: str = Field(min_length=1, max_length=32)
    current_version: str = Field(min_length=1, max_length=32)
    reason: str = Field(default="", max_length=512)


# ---------------------------------------------------------------------------
# Health check / compatibility preflight (E2 Task 3)
# ---------------------------------------------------------------------------


HealthStatus = Literal["compatible", "incompatible", "offline", "unknown"]


class CompatibilityReport(CamelModel):
    """The result of a Directus compatibility preflight at startup.

    ``offline`` is NOT treated as client corruption; the Launcher follows the
    product offline policy (read-only cache / explicit offline page / block).
    """

    status: HealthStatus
    server_version: str | None = Field(default=None, max_length=64)
    missing_capabilities: list[str] = Field(default_factory=list, max_length=64)
    schema_contract_match: bool = True
    message: str = Field(default="", max_length=1024)


class HealthCheckResult(CamelModel):
    """A sanitized health-check result (no token, no sensitive path)."""

    compatible: CompatibilityReport
    timestamp: str = Field(default="", max_length=64)


__all__ = [
    "CamelModel",
    "CompatibilityReport",
    "ComponentHash",
    "DirectusCompatibilityRange",
    "HealthCheckResult",
    "HealthStatus",
    "LauncherPointer",
    "ReleaseManifest",
    "RollbackResult",
    "UpdateRequest",
    "UpdateResult",
    "UpdateState",
]
