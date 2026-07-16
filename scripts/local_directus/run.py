#!/usr/bin/env python3
"""Launch a local Directus 12 + SQLite instance for VibeTable development.

This is the "runtime-distribution" form: Directus is pulled at runtime via
``npm install`` (declared in this directory's ``package.json``), not statically
bundled, and runs against a local SQLite file — so no Docker and no external
database are required. The resulting instance is reached exactly like a remote
one, via ``VIBETABLE_DIRECTUS_URL=http://localhost:<PORT>``.

Workflow (idempotent):

1. Ensure ``node_modules/directus`` exists; run ``npm install`` if not.
2. Materialize ``.env`` from the template and auto-generate KEY/SECRET. Direct
   developer runs also persist a random ADMIN_PASSWORD; app-supplied bootstrap
   passwords are scrubbed after schema setup and live only in Windows secure
   storage.
3. Symlink the built ``vibetable-bulk-mutation`` extension into ``extensions/`` so
   the ``/vibetable-bulk-mutation/apply`` endpoint registers.
4. Start ``directus start`` as a child process and wait for
   ``GET /server/ping`` to answer.
5. On first boot only: log in as admin and apply the VibeTable schema blueprint via
   the existing in-process Directus bootstrapper.

Run with::

    .venv\\Scripts\\python.exe scripts/local_directus/run.py

Stop the server with Ctrl+C (the child process is killed on exit).
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(
    os.environ.get("VIBETABLE_LOCAL_DIRECTUS_ROOT", Path(__file__).resolve().parents[2])
).resolve()
HERE = Path(__file__).resolve().parent
ENV_FILE = HERE / ".env"
ENV_TEMPLATE = HERE / ".env.template"
EXTENSIONS_DIR = HERE / "extensions"
BULK_MUTATION_SRC = ROOT / "directus" / "extensions" / "vibetable-bulk-mutation" / "dist" / "index.js"
BULK_MUTATION_PKG = ROOT / "directus" / "extensions" / "vibetable-bulk-mutation" / "package.json"
DIRECTUS_BLUEPRINT = ROOT / "directus" / "blueprints" / "vibetable-empty.json"
DIRECTUS_MANIFEST = ROOT / "directus" / "capabilities" / "vibetable-empty-capabilities.json"
READY_TIMEOUT_SECONDS = 120.0
PING_INTERVAL_SECONDS = 0.5
DEFAULT_PORT = 8055
# Probe range for automatic port evasion when DEFAULT_PORT is taken.
PORT_PROBE_RANGE = range(DEFAULT_PORT, DEFAULT_PORT + 100)

_GENERATE = "__GENERATE__"


def _port_in_use(port: int, host: str = "127.0.0.1") -> bool:
    """True if something is already listening on ``port``."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(0.25)
        try:
            sock.bind((host, port))
        except OSError:
            return True
    return False


def pick_port(preferred: int) -> int:
    """Return a free port, preferring ``preferred``; auto-evade on conflict."""
    if not _port_in_use(preferred):
        return preferred
    _info(f"port {preferred} is in use; scanning for a free port ...")
    for candidate in PORT_PROBE_RANGE:
        if candidate == preferred:
            continue
        if not _port_in_use(candidate):
            _info(f"auto-selected free port {candidate}")
            return candidate
    _fail(f"no free port found in range {PORT_PROBE_RANGE.start}-{PORT_PROBE_RANGE.stop - 1}")


def _info(msg: str) -> None:
    print(f"[local-directus] {msg}", flush=True)


def _fail(msg: str) -> None:
    print(f"[local-directus] ERROR: {msg}", file=sys.stderr, flush=True)
    raise SystemExit(1)


def _private_npm_env() -> dict[str, str]:
    """Build an npm environment that writes ONLY into app-private directories.

    The distribution form pulls Directus at runtime via ``npm install`` on the
    customer's machine. To avoid polluting their global npm state, every npm
    side-effect is redirected into this folder:

    * ``npm_config_cache``  -> ``./.npm-cache``  (download cache)
    * ``npm_config_prefix`` -> ``./.npm-prefix`` (any ``-g`` install target)
    * ``npm_config_userconfig`` / ``globalconfig`` -> a throwaway empty file

    The install itself is local (no ``-g``), so ``node_modules`` lands in this
    directory; nothing is written to ``%APPDATA%\\npm`` or the system PATH.
    """
    env = os.environ.copy()
    env["npm_config_cache"] = str(HERE / ".npm-cache")
    env["npm_config_prefix"] = str(HERE / ".npm-prefix")
    (HERE / ".npm-cache").mkdir(parents=True, exist_ok=True)
    (HERE / ".npm-prefix").mkdir(parents=True, exist_ok=True)
    # Ignore any user/global npmrc so their settings never leak in or out. npm
    # refuses to load the same file as both user and global config, so use two
    # distinct empty files.
    user_rc = HERE / ".npmrc.user"
    global_rc = HERE / ".npmrc.global"
    user_rc.write_text("# isolated user config: intentionally empty\n", encoding="utf-8")
    global_rc.write_text("# isolated global config: intentionally empty\n", encoding="utf-8")
    env["npm_config_userconfig"] = str(user_rc)
    env["npm_config_globalconfig"] = str(global_rc)
    return env


def ensure_npm_installed() -> None:
    """Pull Directus at runtime via ``npm install`` if not already present.

    Reproducibility note: Directus depends on the native ``isolated-vm`` module.
    On Node 24 + Visual Studio 2026, the npm-bundled node-gyp (v8) cannot detect
    VS 2026 and isolated-vm 5.x cannot compile (Node 24's V8 requires C++20).
    The fix is hard-coded into this launcher so a fresh workstation just works:

    * ``package.json`` pins ``isolated-vm@6.1.2`` via ``overrides`` (C++20).
    * This installer ensures a recent ``node-gyp`` (v13, which recognises VS
      2026) is available and points npm at it while installing.

    Isolation note: all npm writes (cache, prefix, any -g node-gyp) go to
    app-private subdirectories of this folder via :func:`_private_npm_env`, so
    the customer's global npm/Node state is never touched.
    """
    if (HERE / "node_modules" / "directus").is_dir():
        _info("node_modules/directus present, skipping npm install")
        return
    npm = shutil.which("npm") or shutil.which("npm.cmd")
    if not npm:
        _fail("npm not found on PATH; install Node.js 24.x first (see scripts/dev.py)")
    env = _private_npm_env()
    # Ensure a node-gyp that recognises VS 2026 is available for native builds.
    gyp_bin = _ensure_node_gyp(npm, env)
    if gyp_bin:
        # npm@11 dropped the `node_gyp` config key; the supported override is the
        # `npm_config_node_gyp` environment variable, read by run-script.
        env["npm_config_node_gyp"] = str(gyp_bin)
    _info("installing directus@12 (runtime, app-private) via npm install ...")
    proc = subprocess.run(
        [npm, "install"],
        cwd=HERE,
        env=env,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if proc.returncode != 0:
        _fail(f"npm install failed:\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}")
    if not (HERE / "node_modules" / "directus").is_dir():
        _fail("npm install did not produce node_modules/directus")
    _info("directus installed (app-private, no global pollution)")


def _ensure_node_gyp(npm: str, env: dict[str, str]) -> Path | None:
    """Make a recent node-gyp (>=13, recognises VS 2026) available.

    Installed into the app-private prefix (``./.npm-prefix``), never the global
    one. Returns the path to its ``node-gyp.js`` entry, or None if it could not
    be installed (native builds may then fail on a fresh VS-2026 workstation,
    but we do not abort — an already-installed directus tree is still usable).
    """
    private_root = HERE / ".npm-prefix" / "node_modules"
    if private_root.is_dir() and (private_root / "node-gyp" / "bin" / "node-gyp.js").is_file():
        version = _node_gyp_version(private_root / "node-gyp")
        if version and int(version.split(".")[0]) >= 13:
            _info(f"node-gyp {version} (>=13) already available (app-private)")
            return private_root / "node-gyp" / "bin" / "node-gyp.js"
    _info("installing node-gyp@latest (app-private, so native modules build on VS 2026) ...")
    install = subprocess.run(
        [npm, "install", "-g", "node-gyp@latest"],
        cwd=HERE,
        env=env,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if install.returncode != 0:
        _info(f"node-gyp install failed (native builds may fail):\n{install.stderr.strip()}")
        return None
    if private_root.is_dir() and (private_root / "node-gyp" / "bin" / "node-gyp.js").is_file():
        return private_root / "node-gyp" / "bin" / "node-gyp.js"
    return None


def _node_gyp_version(pkg_dir: Path) -> str | None:
    try:
        data = json.loads((pkg_dir / "package.json").read_text(encoding="utf-8"))
        return str(data.get("version") or "")
    except (OSError, json.JSONDecodeError):
        return None


def ensure_directories(env_values: dict[str, str]) -> None:
    """Create the SQLite DB and local-storage dirs referenced by ``.env``.

    SQLite cannot create a file in a non-existent directory, so ``data/`` (and
    any parent of ``DB_FILENAME``) and ``STORAGE_LOCAL_ROOT`` must exist before
    Directus starts.
    """
    db_filename = env_values.get("DB_FILENAME", "./data/directus.sqlite")
    db_path = (HERE / db_filename) if not Path(db_filename).is_absolute() else Path(db_filename)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    storage_root = env_values.get("STORAGE_LOCAL_ROOT", "./uploads")
    storage_path = (
        (HERE / storage_root) if not Path(storage_root).is_absolute() else Path(storage_root)
    )
    storage_path.mkdir(parents=True, exist_ok=True)
    _info(f"ensured dirs: {db_path.parent} , {storage_path}")


def _parse_env(text: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        out[key.strip()] = value.strip()
    return out


def _render_env(values: dict[str, str]) -> str:
    lines = [
        "# Auto-materialized by run.py from .env.template. Edit by hand if needed.",
        "# This file is gitignored; never commit real KEY/SECRET/passwords.",
        "",
    ]
    for key, value in values.items():
        lines.append(f"{key}={value}")
    return "\n".join(lines) + "\n"


def materialize_env() -> dict[str, str]:
    """Create/refresh ``.env``, generating random secrets on first run."""
    template_text = ENV_TEMPLATE.read_text(encoding="utf-8")
    values = _parse_env(template_text)
    generated = False
    for key in ("KEY", "SECRET", "ADMIN_PASSWORD"):
        if values.get(key) == _GENERATE or not values.get(key):
            values[key] = secrets.token_urlsafe(32)
            generated = True
    if ENV_FILE.is_file():
        # Preserve previously generated secrets so重启不会换库；只补缺失项。
        existing = _parse_env(ENV_FILE.read_text(encoding="utf-8"))
        for key in values:
            if key in existing and existing[key] != _GENERATE:
                values[key] = existing[key]
        # Merge any template-only additions (e.g. new keys added later).
        for key, value in existing.items():
            values.setdefault(key, value)
    bootstrap_email = os.environ.pop("VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL", "").strip()
    bootstrap_password = os.environ.pop("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD", "")
    if not (HERE / ".bootstrapped").is_file():
        if bootstrap_email:
            values["ADMIN_EMAIL"] = bootstrap_email
        if bootstrap_password:
            values["ADMIN_PASSWORD"] = bootstrap_password
    ENV_FILE.write_text(_render_env(values), encoding="utf-8")
    if generated:
        _info("generated random KEY/SECRET/ADMIN_PASSWORD into .env (first run)")
    _info(f"env written -> {ENV_FILE}")
    return values


def link_bulk_mutation_extension() -> None:
    """Make the built bulk-mutation endpoint discoverable by Directus."""
    if not BULK_MUTATION_SRC.is_file():
        _fail(
            "bulk-mutation extension not built; run "
            "`npm install && npm run build` in "
            "directus/extensions/vibetable-bulk-mutation first"
        )
    target_dir = EXTENSIONS_DIR / "vibetable-bulk-mutation"
    target_dir.mkdir(parents=True, exist_ok=True)
    # Copy package.json + dist so Directus loads a self-contained endpoint dir.
    shutil.copy2(BULK_MUTATION_PKG, target_dir / "package.json")
    dist_target = target_dir / "dist"
    if dist_target.is_dir():
        shutil.rmtree(dist_target)
    shutil.copytree(BULK_MUTATION_SRC.parent, dist_target)
    _info(f"bulk-mutation extension staged -> {target_dir}")


def _wait_ready(base_url: str) -> None:
    ping_url = f"{base_url.rstrip('/')}/server/ping"
    deadline = time.monotonic() + READY_TIMEOUT_SECONDS
    last_err = ""
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(ping_url, timeout=3) as resp:
                if resp.status == 200:
                    _info(f"directus ready ({ping_url} -> 200)")
                    return
        except (urllib.error.URLError, OSError) as exc:
            last_err = repr(exc)
        time.sleep(PING_INTERVAL_SECONDS)
    _fail(f"directus did not become ready within {READY_TIMEOUT_SECONDS:g}s ({last_err})")


def start_directus(port: str) -> subprocess.Popen[str]:
    env = os.environ.copy()
    # directus reads .env from its working directory.
    env.update(_parse_env(ENV_FILE.read_text(encoding="utf-8")))
    # Bootstrap credentials are not runtime configuration.  Do not retain the
    # administrator password in the long-lived Node process environment.
    env.pop("ADMIN_EMAIL", None)
    env.pop("ADMIN_PASSWORD", None)
    npm = shutil.which("npx") or shutil.which("npx.cmd") or "npx"
    _info(f"starting directus on port {port} ...")
    proc = subprocess.Popen(
        [npm, "directus", "start"],
        cwd=HERE,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    return proc


def bootstrap_database() -> None:
    """Install Directus' internal tables + initial admin (first boot only).

    Directus 12 no longer auto-installs tables on ``start``; ``directus
    bootstrap`` creates the schema migrations, admin role and first admin user
    from the ``ADMIN_EMAIL``/``ADMIN_PASSWORD`` env vars. Idempotent: rerunning
    on an already-bootstrapped DB is a no-op.
    """
    marker = HERE / ".bootstrapped"
    if marker.is_file():
        _info("database already bootstrapped (marker present), skipping")
        return
    npx = shutil.which("npx") or shutil.which("npx.cmd") or "npx"
    env = os.environ.copy()
    env.update(_parse_env(ENV_FILE.read_text(encoding="utf-8")))
    _info("running `directus bootstrap` (install tables + admin) ...")
    proc = subprocess.run(
        [npx, "directus", "bootstrap"],
        cwd=HERE,
        env=env,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    # bootstrap prints INFO lines to stdout; a non-zero exit with "already"
    # wording is fine on a re-run without a marker.
    combined = (proc.stdout or "") + (proc.stderr or "")
    if proc.returncode != 0 and "already" not in combined.lower():
        _fail(
            "directus bootstrap failed:\nstdout:\n"
            f"{proc.stdout}\nstderr:\n{proc.stderr}\n"
            "(common cause: ADMIN_EMAIL not a valid email address)"
        )
    marker.write_text("ok", encoding="utf-8")
    _info("database bootstrapped; wrote marker .bootstrapped")


def _admin_login(base_url: str, email: str, password: str) -> str:
    body = json.dumps({"email": email, "password": password, "mode": "json"}).encode()
    req = urllib.request.Request(
        f"{base_url.rstrip('/')}/auth/login",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    token = payload.get("data", {}).get("access_token")
    if not token:
        _fail(f"admin login returned no access_token: {payload}")
    return token


def apply_schema_if_first_boot(base_url: str, admin_email: str, admin_password: str) -> None:
    """Seed collections/relations/policies on a blank instance.

    Uses the existing bootstrapper in-process so the packaged backend runner
    does not need a second Python interpreter or a loose deployment script.
    """
    marker = HERE / ".schema-applied"
    if marker.is_file():
        _info("schema already applied (marker present), skipping bootstrap")
        return
    _info("logging in as admin to obtain a bootstrap token ...")
    token = _admin_login(base_url, admin_email, admin_password)
    _info("applying VibeTable schema blueprint in-process ...")
    try:
        from backend.adapters.directus.bootstrap import (
            DirectusProjectBootstrapper,
            load_blueprint,
        )
        from backend.adapters.directus.contracts import DirectusSourceConfig
        from backend.adapters.directus.profile import CapabilityManifest
        from backend.adapters.directus.transport import StdlibDirectusTransport

        blueprint = load_blueprint(DIRECTUS_BLUEPRINT)
        manifest = CapabilityManifest.model_validate_json(
            DIRECTUS_MANIFEST.read_text(encoding="utf-8")
        )
        if blueprint["schema_version"] != manifest.schema_version:
            raise ValueError("blueprint and capability manifest schema versions differ")
        config = DirectusSourceConfig(
            url=base_url.rstrip("/"),
            project="local-bootstrap",
            token_ref="environment-only",
        )
        bootstrapper = DirectusProjectBootstrapper(StdlibDirectusTransport(config), token)
        asyncio.run(bootstrapper.apply_empty(blueprint))
    except Exception as exc:
        _fail(
            "schema apply failed: "
            f"{exc}\n(if collections already existed, remove the .schema-applied marker "
            "only after confirming the instance is seeded)"
        )
    marker.write_text(base_url, encoding="utf-8")
    _info("schema applied; wrote marker .schema-applied")


def scrub_bootstrap_password(env_values: dict[str, str]) -> None:
    """Remove the bootstrap password from the persistent plaintext env file."""

    env_values["ADMIN_PASSWORD"] = "__WINDOWS_CREDENTIAL_MANAGER__"
    ENV_FILE.write_text(_render_env(env_values), encoding="utf-8")
    _info("removed bootstrap password from .env")


def _stream_output(proc: subprocess.Popen[str]) -> None:
    assert proc.stdout is not None
    try:
        for line in proc.stdout:
            sys.stdout.write(f"[directus] {line}")
            sys.stdout.flush()
    finally:
        proc.wait()
    _fail(f"directus process exited with code {proc.returncode}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--no-apply",
        action="store_true",
        help="start Directus only; do not bootstrap the VibeTable schema",
    )
    parser.add_argument(
        "--port",
        default=None,
        help="override PORT (default: read from .env / template)",
    )
    args = parser.parse_args()

    app_supplied_bootstrap_password = bool(os.environ.get("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD"))
    ensure_npm_installed()
    env_values = materialize_env()
    requested_port = int(args.port) if args.port else int(env_values.get("PORT", DEFAULT_PORT))
    port = pick_port(requested_port)
    if port != requested_port:
        # Persist the evaded port so subsequent runs are stable & the client
        # launcher reads the same value.
        env_values["PORT"] = str(port)
        ENV_FILE.write_text(_render_env(env_values), encoding="utf-8")
        _info(f"persisted PORT={port} into .env (auto-evaded from {requested_port})")
    base_url = f"http://localhost:{port}"
    link_bulk_mutation_extension()
    ensure_directories(env_values)
    bootstrap_database()

    proc = start_directus(str(port))

    def _cleanup(*_: object) -> None:
        if proc.poll() is None:
            _info("terminating directus ...")
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()

    signal.signal(signal.SIGINT, _cleanup)
    signal.signal(signal.SIGTERM, _cleanup)

    try:
        _wait_ready(base_url)
        if not args.no_apply:
            apply_schema_if_first_boot(
                base_url,
                env_values.get("ADMIN_EMAIL", "admin@local"),
                env_values.get("ADMIN_PASSWORD", ""),
            )
        if app_supplied_bootstrap_password:
            scrub_bootstrap_password(env_values)
        _info("=" * 60)
        _info(f"Directus ready: {base_url}")
        _info(
            "Set the client env then launch VibeTable:\n"
            f'  $env:VIBETABLE_DIRECTUS_URL = "{base_url}"\n'
            f"  $env:VIBETABLE_DIRECTUS_PROJECT = 'default'\n"
            f"  admin: {env_values.get('ADMIN_EMAIL', 'admin@local')}"
        )
        _info("=" * 60)
        _info("streaming directus logs (Ctrl+C to stop) ...")
        _stream_output(proc)
    except BaseException:
        _cleanup()
        raise
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
