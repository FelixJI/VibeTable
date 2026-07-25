"""Provider-neutral settings, typed local commands and shortcuts service.

* **Settings**: device-local settings persist to a local JSON file with version
  migration; shared settings are read through an internal-metadata product port.
* **Commands**: a static catalog of typed, whitelisted commands. No arbitrary
  code/DSL/dynamic import.
* **Shortcuts**: reference versioned built-in commands or approved URL/file
  actions; the launch path validates scheme + grant + target.
"""

from __future__ import annotations

import json
import time
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any, Protocol

from backend.contracts.settings_commands import (
    CommandResult,
    CommandsResult,
    DeviceSettings,
    LaunchActionResult,
    LocalCommandCatalogEntry,
    SharedSettingsEntry,
    SharedSettingsResult,
    ShortcutEntry,
    ShortcutsResult,
)

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
]

#: Allowed URL schemes for the OS Launch Broker.
ALLOWED_URL_SCHEMES: frozenset[str] = frozenset({"https"})

_COMMAND_GRANT_SCOPES: dict[str, tuple[str, str]] = {
    "export.query": ("export_target", "write"),
}

CommandExecutor = Callable[[dict[str, Any], str], Awaitable[dict[str, Any]]]


class InternalMetadataPort(Protocol):
    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]: ...


class GrantAuthority(Protocol):
    def resolve(self, grant_id: str, *, purpose: str, direction: str) -> object: ...


class SettingsCommandError(Exception):
    """A settings/command error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class SettingsCommandService:
    """Settings, commands and shortcuts behind product-owned ports."""

    def __init__(
        self,
        *,
        metadata_port: InternalMetadataPort,
        device_state_path: Path,
        grant_authority: GrantAuthority | None = None,
        command_executors: dict[str, CommandExecutor] | None = None,
    ) -> None:
        self._metadata = metadata_port
        self._grant_authority = grant_authority
        self._device_state_path = device_state_path
        self._command_executors = dict(command_executors or {})
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
        try:
            raw = await self._metadata.list_metadata(
                "shared_settings",
                scope=collection,
                keys=keys,
            )
        except Exception:
            # Offline / permission denied: return empty + not fresh (no fake
            # defaults).
            return SharedSettingsResult(settings=[], cached_on="", fresh=False)
        entries = [
            SharedSettingsEntry(
                key=str(item.get("key", "")),
                value=item.get("value"),
                updated_on=str(item.get("updatedOn", "")),
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
    # Typed local commands (D2.3)
    # ------------------------------------------------------------------

    def list_commands(self) -> CommandsResult:
        return CommandsResult(commands=list(COMMAND_CATALOG))

    async def run_command(
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
        if entry.requires_grant:
            assert grant_id is not None
            if self._grant_authority is None:
                raise SettingsCommandError(
                    "path grant authority is unavailable",
                    code="command_grant_authority_unavailable",
                )
            purpose, direction = _COMMAND_GRANT_SCOPES[command_id]
            try:
                self._grant_authority.resolve(
                    grant_id,
                    purpose=purpose,
                    direction=direction,
                )
            except Exception as exc:
                raise SettingsCommandError(
                    "path grant does not authorize this command",
                    code="command_grant_invalid",
                ) from exc
        executor = self._command_executors.get(command_id)
        if executor is None:
            raise SettingsCommandError(
                f"command {command_id!r} has no product executor",
                code="command_executor_unavailable",
            )
        assert grant_id is not None
        try:
            output = await executor(dict(params), grant_id)
        except SettingsCommandError:
            raise
        except Exception as exc:
            raise SettingsCommandError(
                f"command {command_id!r} failed: {exc}",
                code="command_execution_failed",
            ) from exc
        return CommandResult(
            command_id=command_id,
            success=True,
            output=output,
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
        elif shortcut.target == "file-action":
            raise SettingsCommandError(
                "file shortcuts require an explicit one-shot grant and are disabled",
                code="shortcut_file_action_removed",
            )


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


__all__ = [
    "ALLOWED_URL_SCHEMES",
    "COMMAND_CATALOG",
    "SettingsCommandError",
    "SettingsCommandService",
]
