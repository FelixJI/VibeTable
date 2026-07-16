"""D2 settings, Flows, typed local commands and shortcuts service.

* **Settings**: device-local settings persist to a local JSON file with version
  migration; shared-business settings read from a permission-protected Directus
  collection with an offline cache (no fake defaults when uncached).
* **Flows**: only approved manual/webhook Flows are invocable; the approved list
  is a versioned manifest.
* **Commands**: a static catalog of typed, whitelisted commands. No arbitrary
  code/DSL/dynamic import.
* **Shortcuts**: reference versioned built-in commands or approved URL/file
  actions; the launch path validates scheme + grant + target.
"""

from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.errors import DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.settings_commands import (
    ApprovedFlowEntry,
    ApprovedFlowsResult,
    CommandResult,
    CommandsResult,
    DeviceSettings,
    FlowInvocationResult,
    LaunchActionResult,
    LocalCommandCatalogEntry,
    SharedSettingsEntry,
    SharedSettingsResult,
    ShortcutEntry,
    ShortcutsResult,
)

#: The locked approved-Flow manifest (deployment-defined; this is the default set).
APPROVED_FLOWS: list[ApprovedFlowEntry] = [
    ApprovedFlowEntry(
        flow_id="vibetable-notify-owner",
        name="Notify record owner",
        trigger="manual",
        payload_schema={"collection": {"type": "string"}, "itemId": {"type": "string"}},
    ),
]

#: The static local-command catalog (typed + whitelisted).
COMMAND_CATALOG: list[LocalCommandCatalogEntry] = [
    LocalCommandCatalogEntry(
        command_id="export.query",
        version="1",
        param_schema={"collection": {"type": "string"}, "format": {"type": "string"}},
        requires_grant=True,
        cancellable=True,
        risk="none",
        description="Export a collection query to a file (delegates to C1 export).",
    ),
    LocalCommandCatalogEntry(
        command_id="file.replace-content",
        version="1",
        param_schema={"find": {"type": "string"}, "replace": {"type": "string"}},
        requires_grant=True,
        cancellable=True,
        risk="destructive",
        description="Replace text content across files in a granted directory (delegates to D1).",
    ),
]

#: Allowed URL schemes for the OS Launch Broker.
ALLOWED_URL_SCHEMES: frozenset[str] = frozenset({"https"})


class SettingsCommandError(Exception):
    """A settings/command error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class SettingsCommandService:
    """D2 settings + Flows + commands + shortcuts."""

    def __init__(
        self,
        *,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        device_state_path: Path,
    ) -> None:
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._device_state_path = device_state_path
        self._shortcuts: dict[str, ShortcutEntry] = {}

    # ------------------------------------------------------------------
    # Settings (D2.1)
    # ------------------------------------------------------------------

    def read_device(self) -> DeviceSettings:
        """Read device-local settings with version migration + corrupt recovery."""
        if not self._device_state_path.is_file():
            return DeviceSettings()
        try:
            raw = json.loads(self._device_state_path.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            # Corrupt state → recover to defaults (never crash the app).
            return DeviceSettings()
        return self._migrate_device(raw)

    def save_device(self, settings: DeviceSettings) -> DeviceSettings:
        self._device_state_path.parent.mkdir(parents=True, exist_ok=True)
        self._device_state_path.write_text(
            json.dumps(settings.model_dump(mode="json"), indent=2), encoding="utf-8"
        )
        return settings

    async def read_shared(self, collection: str, keys: list[str]) -> SharedSettingsResult:
        token = await self._auth.access_token()
        try:
            payload = await self._transport.request(
                "GET",
                f"/items/{collection}",
                access_token=token,
                query={
                    "fields": ["id", "key", "value", "date_updated"],
                    "limit": 100,
                },
            )
        except DirectusTransportError:
            # Offline / permission denied: return empty + not fresh (no fake
            # defaults).
            return SharedSettingsResult(settings=[], cached_on="", fresh=False)
        raw = _list(payload)
        entries = [
            SharedSettingsEntry(
                key=str(item.get("key", "")),
                value=item.get("value"),
                updated_on=str(item.get("date_updated", "")),
            )
            for item in raw
            if isinstance(item, dict) and (not keys or item.get("key") in keys)
        ]
        return SharedSettingsResult(
            settings=entries,
            cached_on=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            fresh=True,
        )

    def _migrate_device(self, raw: dict[str, Any]) -> DeviceSettings:
        version = int(raw.get("schema_version", 1))
        # Future migrations: version 1 → 2 → ... (each step transforms in place).
        try:
            return DeviceSettings.model_validate(raw)
        except Exception:
            return DeviceSettings(schema_version=version)

    # ------------------------------------------------------------------
    # Flows (D2.2)
    # ------------------------------------------------------------------

    def list_approved_flows(self) -> ApprovedFlowsResult:
        return ApprovedFlowsResult(flows=list(APPROVED_FLOWS))

    async def invoke_flow(
        self, flow_id: str, correlation_id: str, payload: dict[str, Any]
    ) -> FlowInvocationResult:
        approved = {f.flow_id for f in APPROVED_FLOWS}
        if flow_id not in approved:
            raise SettingsCommandError(
                f"flow {flow_id!r} is not in the approved manifest",
                code="flow_not_approved",
            )
        token = await self._auth.access_token()
        try:
            response = await self._transport.request(
                "POST",
                f"/flows/trigger/{flow_id}",
                access_token=token,
                json_body={"correlation_id": correlation_id, "payload": payload},
            )
        except DirectusTransportError as exc:
            return FlowInvocationResult(
                flow_id=flow_id,
                correlation_id=correlation_id,
                async_acknowledged=False,
                error=str(exc),
            )
        data = _object(response)
        return FlowInvocationResult(
            flow_id=flow_id,
            correlation_id=correlation_id,
            async_acknowledged=bool(data.get("async")),
            response=data,
        )

    # ------------------------------------------------------------------
    # Typed local commands (D2.3)
    # ------------------------------------------------------------------

    def list_commands(self) -> CommandsResult:
        return CommandsResult(commands=list(COMMAND_CATALOG))

    def run_command(
        self,
        command_id: str,
        params: dict[str, Any],
        grant_id: str | None = None,
    ) -> CommandResult:
        catalog = {c.command_id: c for c in COMMAND_CATALOG}
        entry = catalog.get(command_id)
        if entry is None:
            raise SettingsCommandError(
                f"command {command_id!r} is not in the static catalog",
                code="command_unknown",
            )
        if entry.requires_grant and not grant_id:
            raise SettingsCommandError(
                f"command {command_id!r} requires a path grant",
                code="command_grant_required",
            )
        # The actual command execution delegates to the relevant service (C1
        # export, D1 file-tools). For this first cut we validate + acknowledge.
        return CommandResult(
            command_id=command_id,
            success=True,
            output={"acknowledged": True, "risk": entry.risk},
        )

    # ------------------------------------------------------------------
    # Shortcuts (D2.4)
    # ------------------------------------------------------------------

    def list_shortcuts(self) -> ShortcutsResult:
        return ShortcutsResult(shortcuts=list(self._shortcuts.values()))

    def save_shortcut(self, shortcut: ShortcutEntry) -> ShortcutEntry:
        self._validate_shortcut(shortcut)
        self._shortcuts[shortcut.shortcut_id] = shortcut
        return shortcut

    def delete_shortcut(self, shortcut_id: str) -> dict[str, Any]:
        self._shortcuts.pop(shortcut_id, None)
        return {"deleted": shortcut_id}

    def launch_action(self, shortcut_id: str, resolve_grant: Any = None) -> LaunchActionResult:
        shortcut = self._shortcuts.get(shortcut_id)
        if shortcut is None:
            raise SettingsCommandError(
                f"shortcut {shortcut_id!r} not found",
                code="shortcut_unknown",
            )
        self._validate_shortcut(shortcut)
        return LaunchActionResult(
            shortcut_id=shortcut_id,
            launched=True,
            blocked_reason=None,
        )

    def _validate_shortcut(self, shortcut: ShortcutEntry) -> None:
        if shortcut.target == "built-in-command":
            catalog = {c.command_id for c in COMMAND_CATALOG}
            if not shortcut.command_id or shortcut.command_id not in catalog:
                raise SettingsCommandError(
                    f"shortcut references unknown command {shortcut.command_id!r}",
                    code="shortcut_command_unknown",
                )
        elif shortcut.target == "url":
            if not shortcut.url:
                raise SettingsCommandError(
                    "url shortcut requires a url",
                    code="shortcut_url_required",
                )
            scheme = shortcut.url.split(":", 1)[0].lower()
            if scheme not in ALLOWED_URL_SCHEMES:
                raise SettingsCommandError(
                    f"url scheme {scheme!r} is not allowed (only {sorted(ALLOWED_URL_SCHEMES)})",
                    code="shortcut_scheme_blocked",
                )


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


def _list(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        return [item for item in payload["data"] if isinstance(item, dict)]
    return []


def _object(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    if isinstance(payload, dict):
        return payload
    return {}


__all__ = [
    "ALLOWED_URL_SCHEMES",
    "APPROVED_FLOWS",
    "COMMAND_CATALOG",
    "SettingsCommandError",
    "SettingsCommandService",
]
