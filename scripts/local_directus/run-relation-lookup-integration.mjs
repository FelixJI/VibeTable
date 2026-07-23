import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, cpSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const localDirectusRoot = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(localDirectusRoot, "..", "..");
const integrationRoot = join(localDirectusRoot, ".integration");
mkdirSync(integrationRoot, { recursive: true });
const runtimeRoot = mkdtempSync(join(integrationRoot, "directus-12.1.1-"));
const token = `vt-integration-${randomBytes(24).toString("hex")}`;
const password = `Vt-${randomBytes(24).toString("base64url")}!`;
const secret = randomBytes(32).toString("hex");
const directusCli = join(localDirectusRoot, "node_modules", "directus", "cli.js");

function cleanEnvironment(overrides = {}) {
  const environment = { ...process.env, ...overrides };
  if (process.platform === "win32") {
    const pathKeys = Object.keys(environment).filter((key) => key.toLowerCase() === "path");
    for (const duplicate of pathKeys.slice(1)) delete environment[duplicate];
  }
  return environment;
}

function run(command, args, options = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd ?? repositoryRoot,
      env: options.env ?? cleanEnvironment(),
      stdio: options.stdio ?? "inherit",
      windowsHide: true,
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) resolvePromise();
      else reject(new Error(`${command} exited with ${code ?? signal}`));
    });
  });
}

function availablePort() {
  return new Promise((resolvePromise, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        server.close();
        reject(new Error("could not reserve a local Directus port"));
        return;
      }
      server.close((error) => error ? reject(error) : resolvePromise(address.port));
    });
  });
}

function deployExtension(name) {
  const source = join(repositoryRoot, "directus", "extensions", name);
  const target = join(runtimeRoot, "extensions", name);
  mkdirSync(join(target, "dist"), { recursive: true });
  cpSync(join(source, "package.json"), join(target, "package.json"));
  cpSync(join(source, "dist", "index.js"), join(target, "dist", "index.js"));
}

async function waitUntilReady(url, processHandle, output) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (processHandle.exitCode !== null) {
      throw new Error(`Directus exited before readiness\n${output.join("")}`);
    }
    try {
      const response = await fetch(`${url}/server/ping`);
      if (response.ok) return;
    } catch {
      // The socket is expected to refuse connections during startup.
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 250));
  }
  throw new Error(`Directus readiness timed out\n${output.join("")}`);
}

function waitForExit(processHandle, timeoutMilliseconds) {
  if (processHandle.exitCode !== null) return Promise.resolve(true);
  return new Promise((resolvePromise) => {
    let settled = false;
    const finish = (exited) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      processHandle.off("exit", onExit);
      resolvePromise(exited);
    };
    const onExit = () => finish(true);
    const timeout = setTimeout(() => finish(false), timeoutMilliseconds);
    processHandle.once("exit", onExit);
    if (processHandle.exitCode !== null) finish(true);
  });
}

let directusProcess;
try {
  if (!process.env.npm_execpath) {
    throw new Error("run this harness through npm run test:relation-lookup");
  }
  for (const extension of ["vibetable-lookup-query", "vibetable-bulk-mutation"]) {
    await run(process.execPath, [process.env.npm_execpath, "run", "build"], {
      cwd: join(repositoryRoot, "directus", "extensions", extension),
    });
  }
  deployExtension("vibetable-lookup-query");
  deployExtension("vibetable-bulk-mutation");
  writeFileSync(
    join(runtimeRoot, "package.json"),
    JSON.stringify({ name: "vibetable-directus-integration", private: true, type: "module" }),
  );
  mkdirSync(join(runtimeRoot, "data"), { recursive: true });
  mkdirSync(join(runtimeRoot, "uploads"), { recursive: true });
  const port = await availablePort();
  const url = `http://127.0.0.1:${port}`;
  const directusEnvironment = cleanEnvironment({
    HOST: "127.0.0.1",
    PORT: String(port),
    PUBLIC_URL: url,
    KEY: randomBytes(32).toString("hex"),
    SECRET: secret,
    ADMIN_EMAIL: "integration@example.com",
    ADMIN_PASSWORD: password,
    ADMIN_TOKEN: token,
    DB_CLIENT: "sqlite3",
    DB_FILENAME: join(runtimeRoot, "data", "directus.sqlite"),
    EXTENSIONS_PATH: join(runtimeRoot, "extensions"),
    STORAGE_LOCATIONS: "local",
    STORAGE_LOCAL_DRIVER: "local",
    STORAGE_LOCAL_ROOT: join(runtimeRoot, "uploads"),
    WEBSOCKETS_ENABLED: "false",
    ADMIN_ENABLED: "false",
  });

  await run(process.execPath, [directusCli, "bootstrap"], {
    cwd: runtimeRoot,
    env: directusEnvironment,
  });

  const serverOutput = [];
  directusProcess = spawn(process.execPath, [directusCli, "start"], {
    cwd: runtimeRoot,
    env: directusEnvironment,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  for (const stream of [directusProcess.stdout, directusProcess.stderr]) {
    stream.setEncoding("utf8");
    stream.on("data", (chunk) => {
      serverOutput.push(chunk);
      if (serverOutput.length > 200) serverOutput.shift();
    });
  }
  await waitUntilReady(url, directusProcess, serverOutput);

  const python = process.env.VIBETABLE_INTEGRATION_PYTHON ?? "python";
  const testEnvironment = cleanEnvironment({
    VIBETABLE_DIRECTUS_INTEGRATION_URL: url,
    VIBETABLE_DIRECTUS_INTEGRATION_TOKEN: token,
    VIBETABLE_DIRECTUS_INTEGRATION_SQLITE: join(runtimeRoot, "data", "directus.sqlite"),
  });
  try {
    await run(
      python,
      [
        "-m",
        "pytest",
        "-p",
        "no:cacheprovider",
        "tests/backend/integration/test_plugin_directus_12.py",
        "-q",
        "-o",
        "addopts=",
        "--tb=short",
        "-k",
        "native_relations_and_lookup_extension",
      ],
      { cwd: repositoryRoot, env: testEnvironment },
    );
  } catch (error) {
    throw new Error(`${error instanceof Error ? error.message : String(error)}\n${serverOutput.join("")}`);
  }
} finally {
  if (directusProcess?.exitCode === null) {
    directusProcess.kill("SIGTERM");
    const exited = await waitForExit(directusProcess, 5_000);
    if (!exited && directusProcess.exitCode === null) {
      directusProcess.kill("SIGKILL");
      await waitForExit(directusProcess, 5_000);
    }
  }
  const allowedPrefix = `${resolve(integrationRoot)}${sep}`;
  if (!resolve(runtimeRoot).startsWith(allowedPrefix)) {
    throw new Error("refusing to clean a runtime outside scripts/local_directus/.integration");
  }
  rmSync(runtimeRoot, { recursive: true, force: true });
}
