"""D2 settings, Directus Flows, typed local commands and shortcuts contracts.

* **Settings** (D2.1): classified into device-local, user-local, shared-business
  and secret. Device-local (window position, theme, recent files) stays in local
  state with version migration. Shared-business (holidays, calendars) lives in a
  permission-protected Directus collection. Secrets use Windows secure storage.
* **Flows** (D2.2): VibeTable only invokes approved manual/webhook Flow entry points;
  async Flow results arrive via Notifications, not by reading Flow internal
  tables. No desktop DB backup/archive.
* **Local commands** (D2.3): a static catalog of typed, whitelisted commands
  (id, version, param schema, risk level). No arbitrary code/DSL/dynamic import.
* **Shortcuts** (D2.4): reference versioned built-in commands or approved
  URL/file actions; the WPF Launch Broker validates scheme + grant + target.
"""

from __future__ import annotations

from typing import Any, Literal

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
# Settings (D2.1)
# ---------------------------------------------------------------------------


SettingScope = Literal["device-local", "user-local", "shared-business", "secret"]
ThemeMode = Literal["light", "dark", "system"]


class ThemeTokens(CamelModel):
    """Versioned theme tokens shared between WPF and Web (no Qt StyleManager)."""

    mode: ThemeMode = "system"
    accent: str = Field(default="#2563eb", max_length=16)
    background: str = Field(default="#ffffff", max_length=16)
    foreground: str = Field(default="#111827", max_length=16)


class DeviceSettings(CamelModel):
    """Device-local settings (window position, theme, recent files).

    Stored in local state with a version tag for migration. Never in Directus.
    """

    schema_version: int = Field(default=1, ge=1)
    theme: ThemeTokens = Field(default_factory=ThemeTokens)
    window_position: dict[str, int] = Field(default_factory=dict)
    recent_collections: list[str] = Field(default_factory=list, max_length=32)


class SharedSettingsEntry(CamelModel):
    """One shared-business setting (holiday, calendar rule) from Directus."""

    key: str = Field(min_length=1, max_length=128)
    value: Any = None
    updated_on: str = Field(default="", max_length=64)


class ReadSharedSettingsParams(CamelModel):
    """Parameters for ``settings.readShared`` (permission-protected collection)."""

    collection: str = Field(default="vibetable_settings", min_length=1, max_length=128)
    keys: list[str] = Field(default_factory=list, max_length=64)


class SharedSettingsResult(CamelModel):
    """Result of ``settings.readShared``.

    ``cached_on`` is the timestamp of the cache (offline shows cached time; no
    cache means no fake defaults).
    """

    settings: list[SharedSettingsEntry] = Field(default_factory=list)
    cached_on: str = Field(default="", max_length=64)
    fresh: bool = True


class SaveDeviceSettingsParams(CamelModel):
    """Parameters for ``settings.saveDevice``."""

    settings: DeviceSettings


# ---------------------------------------------------------------------------
# Directus Flows (D2.2)
# ---------------------------------------------------------------------------


class InvokeFlowParams(CamelModel):
    """Parameters for ``flow.invoke`` (approved manual/webhook entry only).

    Wire form::

        {"flowId": "flow-1", "correlationId": "uuid", "payload": {...}}

    The flow must be in the approved manifest; VibeTable passes the user session or a
    restricted app identity + correlation id + schema-validated payload.
    """

    flow_id: str = Field(min_length=1, max_length=128)
    correlation_id: str = Field(default="", max_length=128)
    payload: dict[str, Any] = Field(default_factory=dict)


class FlowInvocationResult(CamelModel):
    """Result of ``flow.invoke``.

    Synchronous flows return the server response directly; async flows return
    ``async_acknowledged=true`` and the result arrives via a Notification.
    """

    flow_id: str = Field(min_length=1, max_length=128)
    correlation_id: str = Field(default="", max_length=128)
    async_acknowledged: bool = False
    response: dict[str, Any] = Field(default_factory=dict)
    error: str | None = Field(default=None, max_length=1024)


class ListApprovedFlowsParams(CamelModel):
    """Parameters for ``flow.listApproved`` (the approved manual/webhook list)."""


class ApprovedFlowEntry(CamelModel):
    """One approved Flow the desktop may invoke."""

    flow_id: str = Field(min_length=1, max_length=128)
    name: str = Field(default="", max_length=256)
    trigger: Literal["manual", "webhook"] = "manual"
    payload_schema: dict[str, Any] = Field(default_factory=dict)


class ApprovedFlowsResult(CamelModel):
    """Result of ``flow.listApproved``."""

    flows: list[ApprovedFlowEntry] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# Typed local commands (D2.3)
# ---------------------------------------------------------------------------


CommandRisk = Literal["none", "elevated", "destructive"]


class LocalCommandCatalogEntry(CamelModel):
    """One entry in the static local-command catalog.

    Commands are typed + whitelisted (id, version, param schema, risk). No
    arbitrary code text, DSL or dynamic Python import.
    """

    command_id: str = Field(min_length=1, max_length=64)
    version: str = Field(default="1", max_length=16)
    param_schema: dict[str, Any] = Field(default_factory=dict)
    requires_grant: bool = False
    cancellable: bool = True
    risk: CommandRisk = "none"
    description: str = Field(default="", max_length=512)


class ListCommandsParams(CamelModel):
    """Parameters for ``command.list`` (the static catalog)."""


class CommandsResult(CamelModel):
    """Result of ``command.list``."""

    commands: list[LocalCommandCatalogEntry] = Field(default_factory=list)


class RunCommandParams(CamelModel):
    """Parameters for ``command.run``.

    ``command_id`` must be in the static catalog; ``params`` is validated
    against the command's param schema. ``grant_id`` is required when
    ``requires_grant=true``.
    """

    command_id: str = Field(min_length=1, max_length=64)
    params: dict[str, Any] = Field(default_factory=dict)
    grant_id: str | None = Field(default=None, max_length=128)


class CommandResult(CamelModel):
    """Result of ``command.run``."""

    command_id: str = Field(min_length=1, max_length=64)
    success: bool
    output: dict[str, Any] = Field(default_factory=dict)
    error: str | None = Field(default=None, max_length=1024)


# ---------------------------------------------------------------------------
# Shortcuts + OS Launch Broker (D2.4)
# ---------------------------------------------------------------------------


ShortcutTarget = Literal["built-in-command", "url", "file-action"]


class ShortcutEntry(CamelModel):
    """One user shortcut (references a versioned command or approved action)."""

    shortcut_id: str = Field(min_length=1, max_length=128)
    target: ShortcutTarget
    command_id: str | None = Field(default=None, max_length=64)
    url: str | None = Field(default=None, max_length=2048)
    label: str = Field(default="", max_length=256)
    accelerator: str = Field(default="", max_length=64)


class ListShortcutsParams(CamelModel):
    """Parameters for ``shortcut.list``."""


class ShortcutsResult(CamelModel):
    """Result of ``shortcut.list``."""

    shortcuts: list[ShortcutEntry] = Field(default_factory=list)


class SaveShortcutParams(CamelModel):
    """Parameters for ``shortcut.save``."""

    shortcut: ShortcutEntry


class DeleteShortcutParams(CamelModel):
    """Parameters for ``shortcut.delete``."""

    shortcut_id: str = Field(min_length=1, max_length=128)


class LaunchActionParams(CamelModel):
    """Parameters for ``shortcut.launch`` (OS Launch Broker).

    The broker validates the scheme (only ``https``/``file`` with a grant), the
    target's existence, and asks for user confirmation. Web never calls shell
    directly.
    """

    shortcut_id: str = Field(min_length=1, max_length=128)


class LaunchActionResult(CamelModel):
    """Result of ``shortcut.launch``."""

    shortcut_id: str = Field(min_length=1, max_length=128)
    launched: bool
    blocked_reason: str | None = Field(default=None, max_length=512)


__all__ = [
    "ApprovedFlowEntry",
    "ApprovedFlowsResult",
    "CamelModel",
    "CommandResult",
    "CommandRisk",
    "CommandsResult",
    "DeleteShortcutParams",
    "DeviceSettings",
    "FlowInvocationResult",
    "InvokeFlowParams",
    "LaunchActionParams",
    "LaunchActionResult",
    "ListApprovedFlowsParams",
    "ListCommandsParams",
    "ListShortcutsParams",
    "LocalCommandCatalogEntry",
    "ReadSharedSettingsParams",
    "RunCommandParams",
    "SaveDeviceSettingsParams",
    "SaveShortcutParams",
    "SettingScope",
    "SharedSettingsEntry",
    "SharedSettingsResult",
    "ShortcutEntry",
    "ShortcutTarget",
    "ShortcutsResult",
    "ThemeMode",
    "ThemeTokens",
]
