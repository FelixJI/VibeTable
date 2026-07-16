"""Validate, bootstrap and snapshot the greenfield VibeTable Directus project."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from backend.adapters.directus.bootstrap import (  # noqa: E402
    DirectusProjectBootstrapper,
    load_blueprint,
)
from backend.adapters.directus.contracts import DirectusSourceConfig  # noqa: E402
from backend.adapters.directus.profile import CapabilityManifest  # noqa: E402
from backend.adapters.directus.transport import StdlibDirectusTransport  # noqa: E402

DEFAULT_BLUEPRINT = ROOT / "directus" / "blueprints" / "vibetable-empty.json"
DEFAULT_MANIFEST = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"
DEFAULT_SNAPSHOT = ROOT / "directus" / "snapshots" / "vibetable-empty-postgres.json"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["validate", "plan", "apply", "snapshot"])
    parser.add_argument("--blueprint", type=Path, default=DEFAULT_BLUEPRINT)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--output", type=Path, default=DEFAULT_SNAPSHOT)
    parser.add_argument("--yes", action="store_true", help="required for greenfield apply")
    return parser.parse_args()


async def run(args: argparse.Namespace) -> int:
    blueprint = load_blueprint(args.blueprint)
    manifest = CapabilityManifest.model_validate_json(args.manifest.read_text(encoding="utf-8"))
    if blueprint["schema_version"] != manifest.schema_version:
        raise ValueError("blueprint and capability manifest schema versions differ")
    if args.command == "validate":
        print(json.dumps({"schema_version": manifest.schema_version, "status": "valid"}))
        return 0

    url = os.environ.get("DIRECTUS_URL")
    admin_token = os.environ.get("DIRECTUS_ADMIN_TOKEN")
    if not url or not admin_token:
        raise RuntimeError("DIRECTUS_URL and DIRECTUS_ADMIN_TOKEN are required")
    config = DirectusSourceConfig(url=url, project="deployment", token_ref="environment-only")
    bootstrapper = DirectusProjectBootstrapper(StdlibDirectusTransport(config), admin_token)
    if args.command == "plan":
        actions = await bootstrapper.plan(blueprint)
        print(json.dumps([action.model_dump() for action in actions], indent=2))
        return 0
    if args.command == "apply":
        if not args.yes:
            raise RuntimeError("greenfield apply requires --yes")
        actions = await bootstrapper.apply_empty(blueprint)
        print(json.dumps([action.model_dump() for action in actions], indent=2))
        return 0
    snapshot = await bootstrapper.snapshot()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(snapshot, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    print(str(args.output))
    return 0


def main() -> int:
    return asyncio.run(run(parse_args()))


if __name__ == "__main__":
    raise SystemExit(main())
