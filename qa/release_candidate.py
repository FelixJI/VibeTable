#!/usr/bin/env python3
"""Create and verify the immutable release candidate used by the QA gate."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import stat
import sys
import zipfile
from collections.abc import Iterable
from pathlib import Path, PurePosixPath
from typing import BinaryIO

SCHEMA_VERSION = 1
TREE_DOMAIN = b"vibetable.release-candidate.tree.v1\0"


class CandidateError(RuntimeError):
    """The release candidate is missing, mutable or not bound to its report."""


def _sha256_stream(stream: BinaryIO) -> str:
    digest = hashlib.sha256()
    while chunk := stream.read(1024 * 1024):
        digest.update(chunk)
    return digest.hexdigest()


def sha256_file(path: Path) -> str:
    with path.open("rb") as stream:
        return _sha256_stream(stream)


def _tree_digest(entries: Iterable[tuple[str, int, str]]) -> tuple[str, int]:
    digest = hashlib.sha256(TREE_DOMAIN)
    count = 0
    for relative, size, content_hash in entries:
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(str(size).encode("ascii"))
        digest.update(b"\0")
        digest.update(content_hash.encode("ascii"))
        digest.update(b"\n")
        count += 1
    return digest.hexdigest(), count


def package_tree(root: Path) -> tuple[str, int]:
    package_root = root.resolve()
    if not package_root.is_dir():
        raise CandidateError(f"release candidate directory is missing: {package_root}")
    entries: list[tuple[str, int, str]] = []
    for path in sorted(package_root.rglob("*"), key=lambda item: item.as_posix()):
        if path.is_symlink():
            raise CandidateError(f"release candidate contains a symlink: {path}")
        if not path.is_file():
            continue
        relative = path.relative_to(package_root).as_posix()
        entries.append((relative, path.stat().st_size, sha256_file(path)))
    if not entries:
        raise CandidateError("release candidate contains no files")
    return _tree_digest(entries)


def archive_tree(path: Path) -> tuple[str, int]:
    if not path.is_file():
        raise CandidateError(f"release candidate archive is missing: {path}")
    entries: list[tuple[str, int, str]] = []
    with zipfile.ZipFile(path) as archive:
        infos = [item for item in archive.infolist() if not item.is_dir()]
        names = [item.filename for item in infos]
        if len(names) != len(set(names)):
            raise CandidateError("release candidate archive contains duplicate entries")
        for item in sorted(infos, key=lambda value: value.filename):
            relative = PurePosixPath(item.filename)
            if relative.is_absolute() or ".." in relative.parts or "\\" in item.filename:
                raise CandidateError(f"unsafe release candidate archive entry: {item.filename}")
            if stat.S_ISLNK((item.external_attr >> 16) & 0xFFFF):
                raise CandidateError(
                    f"release candidate archive contains a symlink: {item.filename}"
                )
            with archive.open(item) as stream:
                content_hash = _sha256_stream(stream)
            entries.append((item.filename, item.file_size, content_hash))
    if not entries:
        raise CandidateError("release candidate archive contains no files")
    return _tree_digest(entries)


def create_archive(package_root: Path, archive_path: Path) -> dict[str, object]:
    root = package_root.resolve()
    tree_hash, file_count = package_tree(root)
    archive = archive_path.resolve()
    archive.parent.mkdir(parents=True, exist_ok=True)
    temporary = archive.with_name(archive.name + ".tmp")
    if temporary.exists():
        temporary.unlink()
    try:
        with zipfile.ZipFile(
            temporary,
            "w",
            compression=zipfile.ZIP_DEFLATED,
            compresslevel=9,
            allowZip64=True,
        ) as output:
            for path in sorted(root.rglob("*"), key=lambda item: item.as_posix()):
                if not path.is_file():
                    continue
                relative = path.relative_to(root).as_posix()
                info = zipfile.ZipInfo(relative, date_time=(1980, 1, 1, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                info.create_system = 3
                mode = 0o755 if path.suffix.casefold() == ".exe" else 0o644
                info.external_attr = (stat.S_IFREG | mode) << 16
                with path.open("rb") as source, output.open(info, "w", force_zip64=True) as target:
                    while chunk := source.read(1024 * 1024):
                        target.write(chunk)
        os.replace(temporary, archive)
    finally:
        if temporary.exists():
            temporary.unlink()
    archive_hash = sha256_file(archive)
    archived_tree_hash, archived_file_count = archive_tree(archive)
    if archived_tree_hash != tree_hash or archived_file_count != file_count:
        raise CandidateError("release archive contents do not match the package tree")
    checksum = archive.with_suffix(archive.suffix + ".sha256")
    checksum.write_text(f"{archive_hash}  {archive.name}\n", encoding="ascii")
    return candidate_evidence(root, archive)


def candidate_evidence(package_root: Path, archive_path: Path) -> dict[str, object]:
    root = package_root.resolve()
    archive = archive_path.resolve()
    package_hash, package_files = package_tree(root)
    archived_hash, archived_files = archive_tree(archive)
    if archived_hash != package_hash or archived_files != package_files:
        raise CandidateError("release archive no longer matches the package tree")
    archive_hash = sha256_file(archive)
    checksum_path = archive.with_suffix(archive.suffix + ".sha256")
    expected_checksum = f"{archive_hash}  {archive.name}"
    if (
        not checksum_path.is_file()
        or checksum_path.read_text(encoding="ascii").strip() != expected_checksum
    ):
        raise CandidateError("release archive checksum file is missing or stale")
    return {
        "schemaVersion": SCHEMA_VERSION,
        "packageTreeSha256": package_hash,
        "packageFileCount": package_files,
        "archive": {
            "name": archive.name,
            "sha256": archive_hash,
            "size": archive.stat().st_size,
            "treeSha256": archived_hash,
            "fileCount": archived_files,
            "checksumFile": checksum_path.name,
        },
    }


def verify_eligibility_report(
    package_root: Path,
    archive_path: Path,
    report_path: Path,
) -> dict[str, object]:
    evidence = candidate_evidence(package_root, archive_path)
    try:
        report = json.loads(report_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise CandidateError(f"release eligibility report is invalid: {exc}") from exc
    if report.get("releaseEligible") is not True:
        raise CandidateError("release eligibility report is not eligible")
    if report.get("releaseCandidate") != evidence:
        raise CandidateError("release eligibility report is not bound to this candidate")
    return evidence


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    create = subparsers.add_parser("create")
    verify = subparsers.add_parser("verify")
    for command in (create, verify):
        command.add_argument("--package-root", type=Path, required=True)
        command.add_argument("--archive", type=Path, required=True)
    verify.add_argument("--eligibility-report", type=Path, required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.command == "create":
            evidence = create_archive(args.package_root, args.archive)
        else:
            evidence = verify_eligibility_report(
                args.package_root,
                args.archive,
                args.eligibility_report,
            )
    except CandidateError as exc:
        print(f"[FAIL] release candidate: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(evidence, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
