"""Two-phase manual acceptance for natural retention aging."""

from __future__ import annotations

import argparse
import json
import sys
from collections.abc import Sequence
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import UUID

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from tests.e2e import product_e2e_runner  # noqa: E402

DEFAULT_STATE_PATH = ROOT / "build" / "qa" / "natural-retention-aging" / "state.json"


class NaturalAgingStateError(ValueError):
    """The retained seed state cannot safely resume."""


def load_resume_state(
    state_path: Path,
    package_audit: dict[str, object],
    *,
    now: datetime,
) -> dict[str, object]:
    """Validate the externally retained seed state before any Host is launched."""
    state = _read_state(state_path)
    if state.get("formatVersion") != 1 or state.get("phase") != "seeded":
        raise NaturalAgingStateError("natural-aging state is not a completed seed")
    if package_audit.get("passed") is not True:
        raise NaturalAgingStateError("candidate package audit failed")
    if state.get("packageFingerprint") != package_audit.get("fingerprint"):
        raise NaturalAgingStateError("candidate package does not match the seed")

    completed_at = _utc_timestamp(state.get("completedAt"), "completedAt")
    not_before = _utc_timestamp(state.get("notBefore"), "notBefore")
    if not_before < completed_at + timedelta(hours=24):
        raise NaturalAgingStateError("natural-aging state has a shortened maturity window")
    if _utc_timestamp(now, "now") < not_before:
        raise NaturalAgingStateError("natural-aging state is not mature")

    root = state_path.resolve().parent
    workspace = _fixed_path(state.get("workspaceRoot"), root / "workspace", "workspace root")
    local_data = _fixed_path(state.get("localData"), root / "host" / "local-data", "local data")
    workspace_id = _uuid(state.get("workspaceId"), "workspace UUID")
    _uuid(state.get("olderSnapshotId"), "older snapshot ID")
    _uuid(state.get("newerSnapshotId"), "newer snapshot ID")
    if not product_e2e_runner._natural_aging_workspace_matches(
        workspace, local_data.parent, workspace_id
    ):
        raise NaturalAgingStateError("workspace identity does not match the seed")
    return state


def _read_state(path: Path) -> dict[str, object]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise NaturalAgingStateError("natural-aging state is unavailable") from exc
    if not isinstance(payload, dict):
        raise NaturalAgingStateError("natural-aging state must be an object")
    return payload


def _utc_timestamp(value: object, name: str) -> datetime:
    if isinstance(value, datetime):
        parsed = value
    elif isinstance(value, str):
        try:
            parsed = datetime.fromisoformat(value)
        except ValueError as exc:
            raise NaturalAgingStateError(f"{name} is invalid") from exc
    else:
        raise NaturalAgingStateError(f"{name} is invalid")
    if parsed.tzinfo is None:
        raise NaturalAgingStateError(f"{name} must include a timezone")
    return parsed.astimezone(UTC)


def _fixed_path(value: object, expected: Path, name: str) -> Path:
    if not isinstance(value, str) or not value:
        raise NaturalAgingStateError(f"{name} is invalid")
    candidate = Path(value)
    if not candidate.is_absolute():
        raise NaturalAgingStateError(f"{name} is outside the seed state")
    resolved = candidate.resolve()
    if resolved != expected.resolve():
        raise NaturalAgingStateError(f"{name} is outside the seed state")
    if not expected.exists():
        raise NaturalAgingStateError(f"{name} is unavailable")
    return expected.resolve()


def _uuid(value: object, name: str) -> str:
    if not isinstance(value, str):
        raise NaturalAgingStateError(f"{name} is invalid")
    try:
        return str(UUID(value))
    except ValueError as exc:
        raise NaturalAgingStateError(f"{name} is invalid") from exc


def _state_path(value: Path) -> Path:
    default = DEFAULT_STATE_PATH.resolve()
    candidate = value.resolve()
    if candidate == default:
        return candidate
    attempt = candidate.parent
    number = attempt.name.removeprefix("attempt-")
    if (
        candidate.name == default.name
        and attempt.parent == default.parent
        and attempt.name.startswith("attempt-")
        and number.isdecimal()
        and number != "0"
        and str(int(number)) == number
    ):
        return candidate
    raise NaturalAgingStateError("state path must use the dedicated QA root")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("seed", "resume"))
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE_PATH)
    parser.add_argument("--package-root", type=Path, default=product_e2e_runner.DEFAULT_PACKAGE)
    parser.add_argument("--evidence-root", type=Path)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        state_path = _state_path(args.state)
        audit = product_e2e_runner.audit_package(args.package_root)
        if args.phase == "resume":
            load_resume_state(state_path, audit, now=datetime.now(UTC))
        if audit.get("passed") is not True:
            raise NaturalAgingStateError("candidate package audit failed")
        evidence_root = args.evidence_root or state_path.parent / "evidence"
        result = product_e2e_runner.run_natural_aging_phase(
            phase=args.phase,
            state_path=state_path,
            package_root=args.package_root,
            evidence_root=evidence_root,
            package_audit=audit,
        )
        if result.get("status") != "passed":
            raise NaturalAgingStateError("natural-aging phase failed")
    except (NaturalAgingStateError, OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"[FAIL] natural retention aging: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
