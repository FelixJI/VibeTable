"""Single-use platform ZIP identity verification; never compose a product server."""

from __future__ import annotations

import io
from typing import BinaryIO

from backend.infrastructure.plugin_package import (
    DEFAULT_PACKAGE_POLICY,
    PackagePolicy,
    _normalized_package_digest,
    _read_archive_entries,
)

READY = b"VTPLUGIN_VERIFY 1\n"


def run(source: BinaryIO, output: BinaryIO, error: BinaryIO) -> int:
    output.write(READY)
    output.flush()
    try:
        header = source.read(8)
        if len(header) != 8:
            raise ValueError("incomplete package frame")
        size = int.from_bytes(header, "big")
        if size > PackagePolicy.MAX_PACKAGE_BYTES:
            raise ValueError("package frame exceeds its size limit")
        # EOF is part of the single-request protocol; never accept another request.
        data = source.read(size + 1)
        if len(data) != size:
            raise ValueError("incomplete or excessive package frame")
        with io.BytesIO(data) as archive:
            entries = _read_archive_entries(archive, DEFAULT_PACKAGE_POLICY)
        # Installed identity is the authority. Current manifest/worker admission and
        # mandatory integrity.json rules must not exclude historical originals.
        identity = _normalized_package_digest(entries)
        output.write(identity.encode("ascii") + b"\n")
        output.flush()
        return 0
    except Exception:
        # Archive-controlled messages/paths are not part of the bounded reply.
        error.write(b"Retained package verification failed.\n")
        error.flush()
        return 1
