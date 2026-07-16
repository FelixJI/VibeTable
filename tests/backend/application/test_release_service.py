"""E2 release/launcher/updater service tests."""

from __future__ import annotations

import pytest

from backend.application.release_service import (
    REQUIRED_CAPABILITIES,
    ReleaseError,
    ReleaseService,
)
from backend.contracts.release import (
    DirectusCompatibilityRange,
    LauncherPointer,
    ReleaseManifest,
    UpdateRequest,
)


def _manifest(with_caps: bool = True, with_secret: bool = False) -> ReleaseManifest:
    caps = list(REQUIRED_CAPABILITIES) if with_caps else ["only.one"]
    raw = ReleaseManifest(
        release_version="1.0.0",
        directus_compatibility=DirectusCompatibilityRange(
            required_capabilities=caps,
        ),
        components=[],
    )
    if with_secret:
        # Inject a forbidden field to test the secret guard.
        raw = raw.model_copy(update={"signature": "token=abc123"})
    return raw


# ---------------------------------------------------------------------------
# Manifest validation
# ---------------------------------------------------------------------------


def test_validate_manifest_accepts_full_capabilities() -> None:
    service = ReleaseService()
    manifest = service.validate_manifest(_manifest())
    assert manifest.release_version == "1.0.0"


def test_validate_manifest_rejects_missing_capabilities() -> None:
    service = ReleaseService()
    with pytest.raises(ReleaseError, match="missing required capabilities"):
        service.validate_manifest(_manifest(with_caps=False))


def test_validate_manifest_rejects_secrets() -> None:
    service = ReleaseService()
    with pytest.raises(ReleaseError, match="must not contain"):
        service.validate_manifest(_manifest(with_secret=True))


def test_compute_and_verify_component_hash() -> None:
    service = ReleaseService()
    data = b"component bytes"
    h = service.compute_component_hash("backend", data)
    assert len(h) == 64
    assert service.verify_component("backend", data, h)
    assert not service.verify_component("backend", b"tampered", h)


# ---------------------------------------------------------------------------
# Compatibility preflight
# ---------------------------------------------------------------------------


def test_check_compatibility_compatible() -> None:
    service = ReleaseService()
    service.validate_manifest(_manifest())
    result = service.check_compatibility(
        server_version="12.1.1",
        server_capabilities=list(REQUIRED_CAPABILITIES),
        schema_contract="vibetable-1.0",
    )
    assert result.compatible.status == "compatible"


def test_check_compatibility_incompatible_missing_caps() -> None:
    service = ReleaseService()
    service.validate_manifest(_manifest())
    result = service.check_compatibility(
        server_version="12.1.1",
        server_capabilities=["only.one"],
        schema_contract="vibetable-1.0",
    )
    assert result.compatible.status == "incompatible"
    assert len(result.compatible.missing_capabilities) > 0


def test_check_compatibility_offline_is_not_corruption() -> None:
    service = ReleaseService()
    service.validate_manifest(_manifest())
    result = service.check_compatibility(
        server_version=None,
        server_capabilities=[],
        schema_contract=None,
        offline=True,
    )
    assert result.compatible.status == "offline"


def test_check_compatibility_no_manifest_returns_unknown() -> None:
    service = ReleaseService()
    result = service.check_compatibility(
        server_version="12.1.1",
        server_capabilities=list(REQUIRED_CAPABILITIES),
        schema_contract="vibetable-1.0",
    )
    assert result.compatible.status == "unknown"


# ---------------------------------------------------------------------------
# Updater + rollback
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_request_update_succeeds_and_swaps_pointer() -> None:
    service = ReleaseService()
    service.set_pointer(
        LauncherPointer(
            active_version="1.0.0",
            version_directory="C:/VibeTable/versions/1.0.0",
        )
    )
    result = await service.request_update(UpdateRequest(target_version="1.1.0"))
    assert result.state == "succeeded"
    assert result.previous_version == "1.0.0"
    assert service.read_pointer().active_version == "1.1.0"


@pytest.mark.asyncio
async def test_request_update_failure_does_not_swap() -> None:
    service = ReleaseService()
    service.set_pointer(
        LauncherPointer(active_version="1.0.0", version_directory="C:/VibeTable/versions/1.0.0")
    )

    async def fail_download(_req):  # type: ignore[no-untyped-def]
        raise RuntimeError("network error")

    result = await service.request_update(
        UpdateRequest(target_version="1.1.0"), download_fn=fail_download
    )
    assert result.state == "failed"
    assert "network error" in (result.error or "")
    # Pointer unchanged.
    assert service.read_pointer().active_version == "1.0.0"


@pytest.mark.asyncio
async def test_rollback_restores_previous_version() -> None:
    service = ReleaseService()
    service.set_pointer(
        LauncherPointer(active_version="1.0.0", version_directory="C:/VibeTable/versions/1.0.0")
    )
    await service.request_update(UpdateRequest(target_version="1.1.0"))
    rollback = service.rollback("new version self-check failed")
    assert rollback.rolled_back_to == "1.0.0"
    assert rollback.current_version == "1.1.0"
    assert service.read_pointer().active_version == "1.0.0"


def test_rollback_without_previous_raises() -> None:
    service = ReleaseService()
    service.set_pointer(
        LauncherPointer(active_version="1.0.0", version_directory="C:/VibeTable/versions/1.0.0")
    )
    with pytest.raises(ReleaseError, match="no previous version"):
        service.rollback()
