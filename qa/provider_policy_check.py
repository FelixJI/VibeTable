#!/usr/bin/env python3
"""Validate the provider support matrix used by source and packaged builds."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
SUPPORT_PATH = PROJECT_ROOT / "contracts" / "v2" / "provider-support.json"


def check(
    root: Path = PROJECT_ROOT,
    *,
    support_path: Path | None = None,
) -> list[str]:
    """Return structural policy errors for directory-backed workspace copies.

    The application verifies files in the selected directory. It deliberately
    makes no claim about a sync client's cloud state or a removable device's
    lifecycle, so the matrix must not contain attestation-shaped fields.
    """

    root = root.resolve()
    selected = (
        support_path.resolve() if support_path else root / SUPPORT_PATH.relative_to(PROJECT_ROOT)
    )
    try:
        support = json.loads(selected.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"provider support matrix is invalid: {exc}"]
    if support.get("contractVersion") != "2.0":
        return ["provider support contractVersion must be 2.0"]
    providers = support.get("providers")
    if not isinstance(providers, dict):
        return ["provider support matrix has no providers"]

    errors: list[str] = []
    for field in ("evidenceContract", "evidenceDirectory"):
        if field in support:
            errors.append(f"provider support matrix must not define {field}")
    fixed = providers.get("fixed")
    if not isinstance(fixed, dict) or fixed.get("creation") != "enabled":
        errors.append("fixed provider must be enabled")
    elif fixed.get("coordinationStrength") != "strong":
        errors.append("fixed provider must use strong local coordination")

    network = providers.get("network")
    if not isinstance(network, dict):
        errors.append("network provider policy is missing")
    else:
        if network.get("creation") != "enabled":
            errors.append("network provider must be enabled")
        if network.get("coordinationStrength") != "advisory":
            errors.append("network provider must use advisory coordination")
        if network.get("protocol") != "smb":
            errors.append("network: protocol must be smb")
        if "requiredEvidence" in network or "evidence" in network:
            errors.append("network provider must not define attestation evidence")

    for provider in ("registeredCloud", "userMarkedSync", "removable"):
        policy = providers.get(provider)
        if not isinstance(policy, dict):
            errors.append(f"{provider}: provider policy is missing")
            continue
        if policy.get("creation") != "enabled":
            errors.append(f"{provider}: directory replica provider must be enabled")
        if policy.get("coordinationStrength") != "advisory":
            errors.append(f"{provider}: provider must use advisory coordination")
        if "requiredEvidence" in policy or "evidence" in policy:
            errors.append(f"{provider}: provider must not define attestation evidence")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=PROJECT_ROOT)
    args = parser.parse_args(argv)
    errors = check(args.root)
    if errors:
        print("[FAIL] provider policy:")
        for error in errors:
            print(f"  - {error}")
        return 1
    print("[OK] provider policy")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
