#!/usr/bin/env python3
"""Version, verify, and prepare transactional VibeTable releases."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import queue
import secrets
import shutil
import subprocess
import sys
import threading
import urllib.request
import uuid
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

try:
    from scripts.versioning import (
        VersionError,
        bump_version,
        check_versions,
        read_project_version,
        update_versions,
        validate_version,
    )
except ModuleNotFoundError:  # pragma: no cover - direct execution
    from versioning import (
        VersionError,
        bump_version,
        check_versions,
        read_project_version,
        update_versions,
        validate_version,
    )

REPO_ROOT = Path(__file__).resolve().parents[1]


@dataclass(frozen=True)
class UpgradeTransaction:
    backup_dir: Path
    rollback_binary: Path
    manifest: Path


def _tree_hash(root: Path) -> str:
    digest = hashlib.sha256()
    if not root.exists():
        return digest.hexdigest()
    for path in sorted(item for item in root.rglob("*") if item.is_file()):
        digest.update(path.relative_to(root).as_posix().encode())
        digest.update(path.read_bytes())
    return digest.hexdigest()


def prepare_upgrade(
    *,
    install_dir: Path,
    data_dir: Path,
    current_binary: Path,
) -> UpgradeTransaction:
    """Create rollback artifacts without modifying the current installation."""
    install = install_dir.resolve()
    data = data_dir.resolve()
    binary = current_binary.resolve()
    if not binary.is_file() or install not in binary.parents:
        raise ValueError("current binary must exist inside the install directory")
    if install == data or install in data.parents or data in install.parents:
        raise ValueError("install and data directories must be separate")
    backup_root = data.parent / "upgrade-backups"
    backup_root.mkdir(parents=True, exist_ok=True)
    label = datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    backup = backup_root / f"pre_upgrade_{label}_{uuid.uuid4().hex[:8]}"
    backup.mkdir()
    source_data = data
    if source_data.is_dir():
        shutil.copytree(source_data, backup / "data")
    rollback = backup / "previous" / binary.name
    rollback.parent.mkdir()
    shutil.copy2(binary, rollback)
    manifest = backup / "backup-manifest.json"
    manifest.write_text(
        json.dumps(
            {
                "formatVersion": 1,
                "createdAt": datetime.now(UTC).isoformat(),
                "dataSha256": _tree_hash(source_data),
                "backupDataSha256": _tree_hash(backup / "data"),
                "binarySha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                "policy": {
                    "preserveDataOnUninstall": True,
                    "activateNewBinaryOnlyAfterMigration": True,
                    "retainBackupOnFailure": True,
                },
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    return UpgradeTransaction(
        backup_dir=backup,
        rollback_binary=rollback,
        manifest=manifest,
    )


def verify_upgrade_backup(transaction: UpgradeTransaction) -> None:
    manifest = json.loads(transaction.manifest.read_text(encoding="utf-8"))
    if manifest["backupDataSha256"] != _tree_hash(
        transaction.backup_dir / "data"
    ):
        raise ValueError("upgrade backup data hash mismatch")
    if manifest["binarySha256"] != hashlib.sha256(
        transaction.rollback_binary.read_bytes()
    ).hexdigest():
        raise ValueError("upgrade rollback binary hash mismatch")


def _validate_sidecar_migration(binary: Path, data_dir: Path) -> None:
    """Boot the candidate against a copied data set, verify health, then stop."""
    secret = secrets.token_hex(32)
    environment = os.environ.copy()
    environment.update(
        {
            "VIBETABLE_SIDECAR_SESSION_SECRET": secret,
            "VIBETABLE_SIDECAR_DATA_DIR": str(data_dir),
        }
    )
    process = subprocess.Popen(
        [str(binary)],
        cwd=binary.parent,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    try:
        if process.stdout is None:
            raise ValueError("candidate sidecar has no readiness stream")
        lines: queue.Queue[str] = queue.Queue(maxsize=1)
        threading.Thread(
            target=lambda: lines.put(process.stdout.readline()),
            daemon=True,
        ).start()
        ready = json.loads(lines.get(timeout=30))
        address = ready.get("address")
        if (
            ready.get("contract") != "vibetable.sidecar.ready.v1"
            or not isinstance(address, str)
            or not address.startswith("127.0.0.1:")
        ):
            raise ValueError("candidate sidecar returned invalid readiness")
        health_request = urllib.request.Request(
            f"http://{address}/api/vibetable/v1/health",
            headers={"X-VibeTable-Session": secret},
        )
        with urllib.request.urlopen(health_request, timeout=10) as response:
            health = json.load(response)
            if response.status != 200 or health.get("status") != "ok":
                raise ValueError("candidate migration health check failed")
        shutdown_request = urllib.request.Request(
            f"http://{address}/api/vibetable/v1/shutdown",
            method="POST",
            headers={"X-VibeTable-Session": secret},
        )
        with urllib.request.urlopen(shutdown_request, timeout=10) as response:
            if response.status != 202:
                raise ValueError("candidate sidecar refused validation shutdown")
        if process.wait(timeout=15) != 0:
            raise ValueError("candidate sidecar exited after migration with an error")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=10)
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()


def _replace_tree(source: Path, destination: Path) -> None:
    staged = destination.parent / f".{destination.name}.new-{uuid.uuid4().hex}"
    previous = destination.parent / f".{destination.name}.old-{uuid.uuid4().hex}"
    if source.is_dir():
        shutil.copytree(source, staged)
    else:
        staged.mkdir()
    moved_previous = False
    try:
        if destination.exists():
            os.replace(destination, previous)
            moved_previous = True
        os.replace(staged, destination)
    except Exception:
        if destination.exists():
            shutil.rmtree(destination)
        if moved_previous and previous.exists():
            os.replace(previous, destination)
        raise
    finally:
        if staged.exists():
            shutil.rmtree(staged)
    if previous.exists():
        shutil.rmtree(previous)


def _replace_file(source: Path, destination: Path) -> None:
    staged = destination.with_name(f".{destination.name}.new-{uuid.uuid4().hex}")
    shutil.copy2(source, staged)
    try:
        os.replace(staged, destination)
    finally:
        staged.unlink(missing_ok=True)


def activate_upgrade(
    transaction: UpgradeTransaction,
    *,
    install_dir: Path,
    data_dir: Path,
    current_binary: Path,
    new_binary: Path,
    validator: Callable[[Path, Path], None] = _validate_sidecar_migration,
) -> None:
    """Validate migrations on a copy, then activate binary and data transactionally."""
    verify_upgrade_backup(transaction)
    install = install_dir.resolve()
    data = data_dir.resolve()
    current = current_binary.resolve()
    candidate = new_binary.resolve()
    if not candidate.is_file():
        raise ValueError("new binary does not exist")
    if not current.is_file() or install not in current.parents:
        raise ValueError("current binary must exist inside the install directory")
    if install == data or install in data.parents or data in install.parents:
        raise ValueError("install and data directories must be separate")

    migration_root = transaction.backup_dir / "migration-validation"
    migrated_data = migration_root / "data"
    if migration_root.exists():
        shutil.rmtree(migration_root)
    source_data = transaction.backup_dir / "data"
    if source_data.is_dir():
        shutil.copytree(source_data, migrated_data)
    else:
        migrated_data.mkdir(parents=True)

    manifest = json.loads(transaction.manifest.read_text(encoding="utf-8"))
    manifest["activation"] = {"status": "validating"}
    transaction.manifest.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    try:
        validator(candidate, migrated_data)
        manifest["activation"] = {"status": "activating"}
        transaction.manifest.write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        _replace_tree(migrated_data, data)
        _replace_file(candidate, current)
    except Exception as exc:
        # Both restoration sources were verified before activation. Restore
        # each side independently so a partial activation cannot survive.
        rollback_errors: list[Exception] = []
        try:
            _replace_tree(source_data, data)
        except Exception as rollback_exc:
            rollback_errors.append(rollback_exc)
        try:
            _replace_file(transaction.rollback_binary, current)
        except Exception as rollback_exc:
            rollback_errors.append(rollback_exc)
        manifest["activation"] = {
            "status": "rolledBack",
            "error": str(exc),
        }
        transaction.manifest.write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        if rollback_errors:
            raise ExceptionGroup(
                "upgrade failed and rollback was incomplete",
                [exc, *rollback_errors],
            ) from exc
        raise
    manifest["activation"] = {
        "status": "committed",
        "binarySha256": hashlib.sha256(current.read_bytes()).hexdigest(),
        "dataSha256": _tree_hash(data),
    }
    transaction.manifest.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--major", action="store_true")
    action.add_argument("--minor", action="store_true")
    action.add_argument("--patch", action="store_true")
    action.add_argument("--version")
    action.add_argument("--check", action="store_true")
    action.add_argument("--current", action="store_true")
    action.add_argument("--verify-package", type=Path)
    action.add_argument("--prepare-upgrade", action="store_true")
    action.add_argument("--activate-upgrade", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--commit", action="store_true")
    parser.add_argument("--tag", action="store_true")
    parser.add_argument("--install-dir", type=Path)
    parser.add_argument("--data-dir", type=Path)
    parser.add_argument("--current-binary", type=Path)
    parser.add_argument("--new-binary", type=Path)
    return parser


def _run(command: list[str]) -> None:
    subprocess.run(command, cwd=REPO_ROOT, check=True)


def _ensure_clean_worktree() -> None:
    process = subprocess.run(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if process.stdout.strip():
        raise ValueError(
            "release build/commit/tag requires a clean worktree, including untracked files"
        )


def _check() -> int:
    errors = check_versions(REPO_ROOT)
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


def _target(args: argparse.Namespace, current: str) -> str:
    if args.version:
        return validate_version(args.version)
    for part in ("major", "minor", "patch"):
        if getattr(args, part):
            return bump_version(current, part)
    raise VersionError("version action is missing")


def main(argv: list[str] | None = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    if args.verify_package:
        return subprocess.run(
            [sys.executable, "qa/package_check.py", str(args.verify_package)],
            cwd=REPO_ROOT,
            check=False,
        ).returncode
    if args.prepare_upgrade or args.activate_upgrade:
        if not (args.install_dir and args.data_dir and args.current_binary):
            parser.error(
                "upgrade requires --install-dir, --data-dir and --current-binary"
            )
        transaction = prepare_upgrade(
            install_dir=args.install_dir,
            data_dir=args.data_dir,
            current_binary=args.current_binary,
        )
        verify_upgrade_backup(transaction)
        if args.activate_upgrade:
            if args.new_binary is None:
                parser.error("--activate-upgrade requires --new-binary")
            activate_upgrade(
                transaction,
                install_dir=args.install_dir,
                data_dir=args.data_dir,
                current_binary=args.current_binary,
                new_binary=args.new_binary,
            )
        print(transaction.backup_dir)
        return 0
    current = read_project_version(REPO_ROOT)
    if args.current:
        print(current)
        return 0
    if args.check:
        return _check()
    if args.tag and not args.commit:
        parser.error("--tag requires --commit")
    if args.dry_run and (args.build or args.commit or args.tag):
        parser.error("--dry-run cannot be combined with build/commit/tag")
    try:
        if args.build or args.commit or args.tag:
            _ensure_clean_worktree()
        target = _target(args, current)
        changed = update_versions(REPO_ROOT, target, dry_run=args.dry_run)
        if args.dry_run:
            for path in changed:
                print(path.relative_to(REPO_ROOT))
            return 0
        if _check():
            return 1
        if args.build:
            _run([sys.executable, "scripts/build_next.py", "--release"])
            _run(
                [
                    sys.executable,
                    "qa/package_check.py",
                    "dist/VibeTable.Next",
                ]
            )
        if args.commit:
            _run(
                [
                    "git",
                    "add",
                    *[str(path.relative_to(REPO_ROOT)) for path in changed],
                ]
            )
            _run(["git", "commit", "-m", f"chore: release v{target}"])
        if args.tag:
            _run(["git", "tag", "-a", f"v{target}", "-m", f"VibeTable v{target}"])
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"release failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
