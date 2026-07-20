from __future__ import annotations

import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
EXTENSION_NAME = "vibetable-plugin-bridge"


def test_plugin_bridge_bundle_is_discovered_and_published() -> None:
    extension_root = REPO_ROOT / "directus" / "extensions" / EXTENSION_NAME
    manifest = json.loads(
        (REPO_ROOT / "directus" / "extensions" / "manifest.json").read_text(encoding="utf-8")
    )
    entry = next(item for item in manifest["extensions"] if item["name"] == EXTENSION_NAME)

    assert entry == {
        "name": EXTENSION_NAME,
        "type": "bundle",
        "source": "src/bridge/index.ts",
        "entry": "dist/api.js",
        "directusHost": "^12.1.1",
        "capability": "vibetable-plugin-bridge.v1",
        "stage": "Phase C",
        "description": (
            "Shared confirm/progress operations and authenticated runtime bridge "
            "for VibeTable plugin tasks."
        ),
    }

    package = json.loads((extension_root / "package.json").read_text(encoding="utf-8"))
    config = package["directus:extension"]
    assert config["type"] == "bundle"
    assert config["path"] == {"app": "dist/app.js", "api": "dist/api.js"}
    assert {(item["type"], item["name"]) for item in config["entries"]} == {
        ("endpoint", "vibetable-plugin-bridge"),
        ("operation", "vibetable-confirm"),
        ("operation", "vibetable-progress"),
    }

    publish_layout = json.loads(
        (REPO_ROOT / "desktop" / "publish-layout.json").read_text(encoding="utf-8")
    )
    assert {item["name"] for item in publish_layout["components"]["directusExtensions"]} >= {
        EXTENSION_NAME
    }
    assert f"directus/extensions/{EXTENSION_NAME}" in publish_layout["launch"]["directusExtensions"]


def test_plugin_bridge_bundle_exposes_the_versioned_public_capabilities() -> None:
    source_root = REPO_ROOT / "directus" / "extensions" / EXTENSION_NAME / "src"
    confirm_api = (source_root / "confirm" / "api.ts").read_text(encoding="utf-8")
    progress_api = (source_root / "progress" / "api.ts").read_text(encoding="utf-8")
    endpoint = (source_root / "bridge" / "index.ts").read_text(encoding="utf-8")

    assert 'id: "vibetable.confirm@1"' in confirm_api
    assert 'id: "vibetable.progress@1"' in progress_api
    assert "registerBridgeRoutes" in endpoint
