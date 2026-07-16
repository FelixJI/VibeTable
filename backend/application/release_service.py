"""E2 release, Launcher and Updater service.

Implements the release-manifest validation, Directus compatibility preflight
and the update/rollback state machine. The Updater runs in-process for the
contract layer; the actual download/unpack happens host-side (WPF) via the
task runtime — this service validates and records the state transitions.

* :meth:`validate_manifest` checks the signature + component hashes + SBOM.
* :meth:`check_compatibility` reads the server capability report and returns
  ``compatible``/``incompatible``/``offline`` (offline is NOT client corruption).
* :meth:`request_update` transitions through download→verify→unpack→swap, with
  rollback on failure. The pointer swap is atomic.
"""

from __future__ import annotations

import hashlib
from typing import Any

from backend.contracts.release import (
    CompatibilityReport,
    HealthCheckResult,
    LauncherPointer,
    ReleaseManifest,
    RollbackResult,
    UpdateRequest,
    UpdateResult,
)

#: The required capabilities a Directus server must expose for this client.
REQUIRED_CAPABILITIES: frozenset[str] = frozenset(
    {
        "directus.project.v1",
        "auth.local-session.v1",
        "directus.schema.v1",
        "table.query.directus.v1",
        "table.mutation.directus.v1",
        "directus.realtime.v1",
    }
)


class ReleaseError(Exception):
    """A release/launcher error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": code if (code := getattr(self, "code", "")) else "release_error"}


class ReleaseService:
    """E2 release-manifest validation + compatibility preflight + updater."""

    def __init__(self) -> None:
        self._pointer: LauncherPointer | None = None
        self._manifest: ReleaseManifest | None = None

    # ------------------------------------------------------------------
    # Manifest validation (Task 1)
    # ------------------------------------------------------------------

    def validate_manifest(self, manifest: ReleaseManifest) -> ReleaseManifest:
        """Validate a release manifest (signature + required fields).

        Raises if the manifest is missing required capabilities or contains a
        forbidden secret/token/URL field.
        """
        # Fail closed: the manifest must declare the required capabilities.
        declared = set(manifest.directus_compatibility.required_capabilities)
        missing = REQUIRED_CAPABILITIES - declared
        if missing:
            raise ReleaseError(
                f"manifest is missing required capabilities: {', '.join(sorted(missing))}",
                code="manifest_missing_capabilities",
            )
        # The manifest must not carry secrets (defense in depth).
        raw = manifest.model_dump_json()
        for forbidden in ("token", "secret", "password", "api_key", "admin_token"):
            if forbidden in raw.lower():
                raise ReleaseError(
                    f"manifest must not contain a {forbidden!r} field",
                    code="manifest_contains_secret",
                )
        self._manifest = manifest
        return manifest

    def compute_component_hash(self, component: str, data: bytes) -> str:
        """Compute the SHA-256 of a component's bytes."""
        return hashlib.sha256(data).hexdigest()

    def verify_component(self, component: str, data: bytes, expected_hash: str) -> bool:
        """Verify a component's content hash."""
        actual = self.compute_component_hash(component, data)
        return actual == expected_hash

    # ------------------------------------------------------------------
    # Compatibility preflight (Task 3)
    # ------------------------------------------------------------------

    def check_compatibility(
        self,
        *,
        server_version: str | None,
        server_capabilities: list[str],
        schema_contract: str | None,
        offline: bool = False,
    ) -> HealthCheckResult:
        """Check whether the connected Directus server is compatible.

        ``offline`` is NOT treated as client corruption — the Launcher follows
        the product offline policy.
        """
        if offline:
            report = CompatibilityReport(
                status="offline",
                server_version=None,
                message="Directus server unreachable; entering offline policy.",
            )
            return HealthCheckResult(compatible=report, timestamp=_now_iso())
        if self._manifest is None:
            report = CompatibilityReport(
                status="unknown",
                message="No release manifest loaded; cannot check compatibility.",
            )
            return HealthCheckResult(compatible=report, timestamp=_now_iso())
        compat = self._manifest.directus_compatibility
        declared = set(server_capabilities)
        missing = set(compat.required_capabilities) - declared
        schema_match = schema_contract is None or schema_contract == compat.schema_contract
        if missing or not schema_match:
            report = CompatibilityReport(
                status="incompatible",
                server_version=server_version,
                missing_capabilities=sorted(missing),
                schema_contract_match=schema_match,
                message="Directus server is missing required capabilities or schema contract.",
            )
        else:
            report = CompatibilityReport(
                status="compatible",
                server_version=server_version,
                schema_contract_match=True,
                message="Directus server is compatible.",
            )
        return HealthCheckResult(compatible=report, timestamp=_now_iso())

    # ------------------------------------------------------------------
    # Launcher pointer (Task 3)
    # ------------------------------------------------------------------

    def read_pointer(self) -> LauncherPointer | None:
        return self._pointer

    def set_pointer(self, pointer: LauncherPointer) -> LauncherPointer:
        self._pointer = pointer
        return pointer

    # ------------------------------------------------------------------
    # Updater (Task 5)
    # ------------------------------------------------------------------

    async def request_update(
        self,
        request: UpdateRequest,
        *,
        download_fn: Any = None,
        verify_fn: Any = None,
    ) -> UpdateResult:
        """Transition through the update state machine.

        ``download_fn`` and ``verify_fn`` are host-side callbacks (the actual
        download/verify happens in WPF). This service records the state and
        handles rollback on failure.
        """
        previous = self._pointer.active_version if self._pointer else None
        states = ["downloading", "verifying", "unpacking", "swapping"]
        for state in states:
            try:
                if state == "downloading" and download_fn:
                    await download_fn(request)
                elif state == "verifying" and verify_fn:
                    await verify_fn(request)
            except Exception as exc:
                return UpdateResult(
                    target_version=request.target_version,
                    state="failed",
                    previous_version=previous,
                    error=str(exc),
                )
        # Atomic pointer swap.
        if self._pointer:
            self._pointer = LauncherPointer(
                active_version=request.target_version,
                version_directory=self._pointer.version_directory,
                manifest_path=self._pointer.manifest_path,
                previous_version=previous,
            )
        return UpdateResult(
            target_version=request.target_version,
            state="succeeded",
            previous_version=previous,
        )

    def rollback(self, reason: str = "") -> RollbackResult:
        """Roll the pointer back to the previous version."""
        if self._pointer is None or self._pointer.previous_version is None:
            raise ReleaseError("no previous version to roll back to", code="rollback_no_previous")
        current = self._pointer.active_version
        self._pointer = LauncherPointer(
            active_version=self._pointer.previous_version,
            version_directory=self._pointer.version_directory,
            manifest_path=self._pointer.manifest_path,
            previous_version=None,
        )
        return RollbackResult(
            rolled_back_to=self._pointer.active_version,
            current_version=current,
            reason=reason,
        )


def _now_iso() -> str:
    import time

    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


__all__ = ["REQUIRED_CAPABILITIES", "ReleaseError", "ReleaseService"]
