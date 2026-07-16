"""Packaged entry point for the app-private local Directus runtime.

The release already ships a Nuitka standalone backend.  Reuse that embedded
Python runtime to execute the source-only ``local-directus/run.py`` instead of
shipping a second general-purpose Python distribution.
"""

from __future__ import annotations

# These imports are intentionally explicit.  ``run.py`` is a shipped data file
# executed with runpy, so Nuitka cannot discover its standard-library imports
# statically unless this compiled module makes them visible.
import argparse  # noqa: F401
import asyncio  # noqa: F401
import json  # noqa: F401
import os
import runpy
import secrets  # noqa: F401
import shutil  # noqa: F401
import signal  # noqa: F401
import socket  # noqa: F401
import subprocess  # noqa: F401
import sys
import time  # noqa: F401
import urllib.error
import urllib.request  # noqa: F401
from pathlib import Path

# Keep the modules used by run.py's in-process schema bootstrap in the Nuitka
# module graph as well.
from backend.adapters.directus.bootstrap import (  # noqa: F401
    DirectusProjectBootstrapper,
    load_blueprint,
)
from backend.adapters.directus.contracts import DirectusSourceConfig  # noqa: F401
from backend.adapters.directus.profile import CapabilityManifest  # noqa: F401
from backend.adapters.directus.transport import StdlibDirectusTransport  # noqa: F401


def run_local_directus(runtime_directory: str, resource_root: str | None = None) -> None:
    """Execute the shipped runner against ``runtime_directory``.

    The directory's parent is the immutable release root containing
    ``directus/blueprints``, ``directus/capabilities`` and the built extension.
    Per-machine Directus state remains below ``runtime_directory``.
    """

    runtime = Path(runtime_directory).resolve()
    runner = runtime / "run.py"
    if not runner.is_file():
        raise FileNotFoundError(f"local Directus runner is missing: {runner}")

    root = Path(resource_root).resolve() if resource_root else runtime.parent
    os.environ["VIBETABLE_LOCAL_DIRECTUS_ROOT"] = str(root)
    previous_argv = sys.argv
    try:
        sys.argv = [str(runner)]
        runpy.run_path(str(runner), run_name="__main__")
    finally:
        sys.argv = previous_argv


__all__ = ["run_local_directus"]
