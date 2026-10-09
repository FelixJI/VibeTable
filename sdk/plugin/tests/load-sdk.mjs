// Compile the existing SDK for Node tests without adding a runtime dependency.
import { mkdir, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { pathToFileURL, fileURLToPath } from "node:url";
import path from "node:path";

const output = fileURLToPath(new URL("../../../build/plugin-sdk/", import.meta.url));
await mkdir(output, { recursive: true });
await writeFile(path.join(output, "package.json"), '{"type":"module"}\n');
execFileSync(process.execPath, [fileURLToPath(new URL("../node_modules/typescript/bin/tsc", import.meta.url)),
  "--project", fileURLToPath(new URL("../tsconfig.json", import.meta.url)), "--rootDir", fileURLToPath(new URL("../src/", import.meta.url)), "--noEmit", "false", "--outDir", output],
  { stdio: "inherit" });
export const sdk = await import(pathToFileURL(path.join(output, "index.js")));
export const testing = await import(pathToFileURL(path.join(output, "testing.js")));
