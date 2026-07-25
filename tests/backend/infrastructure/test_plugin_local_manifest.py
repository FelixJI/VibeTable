from __future__ import annotations

import json
from typing import Any

import pytest

from backend.infrastructure.plugin_package import (
    PluginPackageError,
    validate_plugin_manifest,
)


def _entries(**manifest_updates: Any) -> list[tuple[str, bytes]]:
    manifest: dict[str, Any] = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.local-worker",
        "version": "1.0.0",
        "displayName": {"en": "Local worker"},
        "compatibility": {
            "minHostVersion": "1.0.0",
            "pluginApi": "1.x",
        },
        "permissions": {
            "data": [
                {
                    "collection": "$active",
                    "operations": ["read", "update"],
                    "fields": ["$configured"],
                }
            ],
            "files": [],
            "privateStorage": True,
            "network": {
                "domains": ["api.example.com"],
                "methods": ["GET", "POST"],
            },
        },
        "actions": [
            {
                "actionId": "run",
                "displayName": {"en": "Run"},
                "mode": "local",
                "risk": "write",
                "workerEntry": "dist/workers/run.js",
            }
        ],
        "ui": {"customViews": []},
    }
    manifest.update(manifest_updates)
    return [
        ("manifest.json", json.dumps(manifest).encode()),
        ("dist/workers/run.js", b"export default async function run() {}"),
    ]


def test_accepts_local_worker_with_mutation_and_network_capabilities() -> None:
    manifest = validate_plugin_manifest(_entries())

    assert manifest["actions"][0]["mode"] == "local"
    assert manifest["permissions"]["network"]["domains"] == ["api.example.com"]


@pytest.mark.parametrize(
    ("update", "path"),
    [
        ({"flows": []}, "flows"),
        ({"flowBindings": []}, "flowBindings"),
        (
            {
                "compatibility": {
                    "minHostVersion": "1.0.0",
                    "pluginApi": "1.x",
                    "dire" "ctus": ">=12 <13",
                }
            },
            "compatibility." + "dire" "ctus",
        ),
    ],
)
def test_rejects_legacy_top_level_fields(
    update: dict[str, Any],
    path: str,
) -> None:
    with pytest.raises(PluginPackageError) as captured:
        validate_plugin_manifest(_entries(**update))

    assert captured.value.code == "legacy_manifest_field"
    assert captured.value.path == path


@pytest.mark.parametrize("mode", ["flow", "hybrid"])
def test_rejects_legacy_action_modes(mode: str) -> None:
    actions = [
        {
            "actionId": "run",
            "displayName": {"en": "Run"},
            "mode": mode,
            "risk": "write",
            "workerEntry": "dist/workers/run.js",
        }
    ]

    with pytest.raises(PluginPackageError) as captured:
        validate_plugin_manifest(_entries(actions=actions))

    assert captured.value.code == "action_invalid"


def test_rejects_entry_flow_even_on_local_action() -> None:
    actions = [
        {
            "actionId": "run",
            "displayName": {"en": "Run"},
            "mode": "local",
            "risk": "write",
            "workerEntry": "dist/workers/run.js",
            "entryFlow": "legacy",
        }
    ]

    with pytest.raises(PluginPackageError) as captured:
        validate_plugin_manifest(_entries(actions=actions))

    assert captured.value.code == "legacy_manifest_field"
    assert captured.value.path == "actions.run.entryFlow"
