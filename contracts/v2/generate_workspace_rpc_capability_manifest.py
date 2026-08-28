"""Generate the Desktop workspace-v2 RPC capability manifest.

The workspace RPC registry remains authoritative for method names and wire
scopes. This generator joins that registry with the reviewed renderer/host
audience policy and emits the single artifact consumed by Desktop.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

try:
    from contracts.v2.generate_rpc_catalog import RPC_REGISTRY
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from generate_rpc_catalog import RPC_REGISTRY


ROOT = Path(__file__).parents[2]
POLICY = ROOT / "contracts" / "v2" / "workspace-rpc-capability-policy.json"
OUTPUT = ROOT / "contracts" / "v2" / "workspace-rpc-capability-manifest.json"
AUDIENCES = frozenset({"rendererPublic", "rendererInternal", "hostOnly"})
POLICY_FIELDS = frozenset({"contractVersion", "methods"})
ENTRY_FIELDS = frozenset({"method", "capabilityId", "audience"})


def _closed_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON property: {key}")
        result[key] = value
    return result


def _load_policy(policy_path: Path) -> dict[str, object]:
    policy = json.loads(
        policy_path.read_text(encoding="utf-8"),
        object_pairs_hook=_closed_object,
    )
    if not isinstance(policy, dict) or set(policy) != POLICY_FIELDS:
        raise ValueError("workspace RPC capability policy has unknown or missing fields")
    return policy


def build_manifest(policy_path: Path = POLICY) -> dict[str, object]:
    policy = _load_policy(policy_path)
    if policy.get("contractVersion") != "2.0":
        raise ValueError("workspace RPC capability policy contractVersion must be 2.0")
    entries = policy.get("methods")
    if not isinstance(entries, list):
        raise ValueError("workspace RPC capability policy methods must be an array")

    registry = {rpc.method: rpc.scope for rpc in RPC_REGISTRY}
    by_method: dict[str, dict[str, object]] = {}
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != ENTRY_FIELDS:
            raise ValueError("workspace RPC capability policy entry has unknown or missing fields")
        method = entry.get("method")
        if not isinstance(method, str) or not method:
            raise ValueError("workspace RPC capability policy method is invalid")
        capability_id = entry.get("capabilityId")
        if not isinstance(capability_id, str) or not capability_id:
            raise ValueError("workspace RPC capability policy capabilityId is invalid")
        if entry.get("audience") not in AUDIENCES:
            raise ValueError("workspace RPC capability policy audience is invalid")
        if method in by_method:
            raise ValueError(f"duplicate policy method: {method}")
        by_method[method] = {
            "method": method,
            "capabilityId": capability_id,
            "audience": entry["audience"],
        }

    missing = sorted(registry.keys() - by_method.keys())
    if missing:
        raise ValueError(f"missing policy methods: {missing}")
    unknown = sorted(by_method.keys() - registry.keys())
    if unknown:
        raise ValueError(f"unknown policy methods: {unknown}")
    return {
        "contractVersion": "2.0",
        "methods": [
            {
                "method": rpc.method,
                "scope": rpc.scope,
                "capabilityId": by_method[rpc.method]["capabilityId"],
                "audience": by_method[rpc.method]["audience"],
            }
            for rpc in RPC_REGISTRY
        ],
    }


def _encoded(policy_path: Path = POLICY) -> str:
    return json.dumps(build_manifest(policy_path), ensure_ascii=False, indent=2) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    expected = _encoded()
    if args.check:
        actual = OUTPUT.read_text(encoding="utf-8") if OUTPUT.exists() else ""
        if actual != expected:
            raise SystemExit("contracts/v2/workspace-rpc-capability-manifest.json is stale")
        return 0
    OUTPUT.write_text(expected, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
