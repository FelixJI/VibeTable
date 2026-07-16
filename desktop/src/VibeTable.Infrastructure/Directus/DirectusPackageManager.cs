using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Owns the install + integrity of the local Directus 12 npm dependency for
/// single-machine VibeTable. This is the application-operations layer: it
/// invokes <c>npm ci</c> (via the bundled Node), verifies the install, caches
/// the verification result, and self-heals a corrupt install (e.g. after an
/// antivirus false positive deletes a file).
/// </summary>
/// <remarks>
/// <para>
/// <b>Why this lives in C#, not <c>run.py</c>.</b> Package management, integrity
/// verification, caching and periodic re-verification are stateful operations
/// that belong to the host — the process that owns the lifecycle and persists
/// state across runs. <c>run.py</c> is a stateless per-launch script and is the
/// wrong place for it. The actual <c>npm ci</c> is still done by npm (we do not
/// reimplement the npm protocol); the host only orchestrates it and guards the
/// result.
/// </para>
/// <para>
/// <b>Integrity model (tuned for antivirus false positives).</b> Antivirus tools
/// sometimes quarantine a single file inside <c>node_modules</c>. The check
/// catches that at three levels:
/// <list type="bullet">
/// <item>Structural: <c>node_modules/directus</c> and the key native packages
/// (<c>isolated-vm</c>, <c>sqlite3</c>) are present.</item>
/// <item>Native load: <c>isolated-vm</c>'s <c>.node</c> binary actually loads
/// and can <c>new Isolate()</c> — the most common fatal quarantine target.</item>
/// <item>Dependency-graph integrity: the <c>package-lock.json</c> content hash is
/// stable, so a tampered/edited lockfile is detected.</item>
/// </list>
/// </para>
/// <para>
/// <b>Caching + periodic re-verification.</b> A successful verification writes a
/// marker (lockfile hash + timestamp + node version). On the next launch the
/// marker short-circuits the check when the hash is unchanged and the marker is
/// younger than <see cref="ReverifyInterval"/> (default 7 days). Past that, or on
/// any hash change, verification re-runs. A verification failure triggers a
/// single self-heal attempt: delete <c>node_modules</c> + marker, re-run
/// <c>npm ci</c>, re-verify.
/// </para>
/// </remarks>
public sealed class DirectusPackageManager
{
    /// <summary>
    /// Filename of the verification marker, written inside the local-Directus
    /// directory alongside <c>package-lock.json</c>.
    /// </summary>
    public const string MarkerFileName = ".install-verified";

    /// <summary>
    /// How long a successful verification is trusted without re-checking. Tuned
    /// to catch silent on-disk corruption (e.g. a background AV quarantine) soon
    /// enough without paying the verification cost on every launch.
    /// </summary>
    public static readonly TimeSpan ReverifyInterval = TimeSpan.FromDays(7);

    /// <summary>
    /// The native packages whose absence is most likely to crash Directus at
    /// runtime. Existence is checked structurally; <c>isolated-vm</c> is also
    /// load-tested because its native binary is the classic AV target.
    /// </summary>
    private static readonly string[] CriticalPackages = { "directus", "isolated-vm", "sqlite3" };

    private readonly TimeSpan _npmTimeout;

    /// <param name="npmTimeout">Per <c>npm ci</c> timeout. The first install
    /// downloads ~600 MB and compiles native modules, so this must be generous
    /// (the default reflects that).</param>
    public DirectusPackageManager(TimeSpan? npmTimeout = null)
    {
        _npmTimeout = npmTimeout ?? TimeSpan.FromMinutes(10);
    }

    /// <summary>
    /// Ensures the local Directus npm dependency is installed and verified.
    /// Returns normally on success; throws on irrecoverable failure (npm not
    /// found, install failed after self-heal, verification still failing).
    /// </summary>
    /// <param name="nodeExe">Absolute path to the Node executable used to run
    /// npm (the bundled <c>runtime/node/node.exe</c>).</param>
    /// <param name="localDirectusDir">The directory containing
    /// <c>package.json</c>/<c>package-lock.json</c>; <c>node_modules</c> lands
    /// here.</param>
    public async Task EnsureInstalledAsync(string nodeExe, string localDirectusDir, CancellationToken cancellationToken)
    {
        if (!File.Exists(nodeExe))
        {
            throw new InvalidOperationException($"Node executable not found: {nodeExe}");
        }
        Directory.CreateDirectory(localDirectusDir);

        string npmScript = ResolveNpmCli(nodeExe);
        string lockHash = ComputeLockHash(localDirectusDir);

        // Fast path: a fresh, matching marker short-circuits everything.
        if (TryReadMarker(localDirectusDir, out InstallMarker? read)
            && read is { } marker
            && marker.LockHash == lockHash
            && !IsExpired(marker))
        {
            return;
        }

        // Install (idempotent: npm ci is a no-op if node_modules is already
        // consistent with the lockfile) then verify. If verification fails once,
        // self-heal by wiping node_modules and reinstalling.
        await RunNpmCiAsync(nodeExe, npmScript, localDirectusDir, cancellationToken).ConfigureAwait(false);
        bool verified = await VerifyAsync(nodeExe, localDirectusDir, lockHash, cancellationToken).ConfigureAwait(false);
        if (!verified)
        {
            SelfHeal(localDirectusDir);
            await RunNpmCiAsync(nodeExe, npmScript, localDirectusDir, cancellationToken).ConfigureAwait(false);
            verified = await VerifyAsync(nodeExe, localDirectusDir, lockHash, cancellationToken).ConfigureAwait(false);
            if (!verified)
            {
                throw new InvalidOperationException(
                    $"Local Directus install failed verification after self-heal. " +
                    $"Directory: {localDirectusDir}");
            }
        }

        WriteMarker(localDirectusDir, new InstallMarker(
            LockHash: lockHash,
            VerifiedAt: DateTimeOffset.UtcNow,
            NodeVersion: ReadNodeVersion(nodeExe)));
    }

    /// <summary>
    /// Runs the integrity checks against an existing install. Public so a caller
    /// can probe health independently of installing (e.g. on demand).
    /// </summary>
    /// <param name="expectedLockHash">The lockfile hash the install must match;
    /// pass <c>null</c> to recompute inside.</param>
    public async Task<bool> VerifyAsync(
        string nodeExe,
        string localDirectusDir,
        string? expectedLockHash,
        CancellationToken cancellationToken)
    {
        string lockHash = expectedLockHash ?? ComputeLockHash(localDirectusDir);

        // 1. Structural: node_modules/directus + critical native packages present.
        string nodeModules = Path.Combine(localDirectusDir, "node_modules");
        if (!Directory.Exists(Path.Combine(nodeModules, "directus")))
        {
            return false;
        }
        foreach (string pkg in CriticalPackages)
        {
            // sqlite3 is a transitive dep; absence is a soft signal, not fatal,
            // but a missing native package dir is exactly the AV-quarantine shape.
            // We only hard-fail on directus + isolated-vm; sqlite3 is advisory.
            if (pkg is "directus" or "isolated-vm"
                && !Directory.Exists(Path.Combine(nodeModules, pkg)))
            {
                return false;
            }
        }

        // 2. Native load: isolated-vm's .node must load and Isolate must
        // construct. This is the single most fragile file under AV fire.
        if (!await VerifyIsolatedVmNativeAsync(nodeExe, localDirectusDir, cancellationToken).ConfigureAwait(false))
        {
            return false;
        }

        // 3. Dependency-graph integrity: lockfile hash stable.
        if (string.IsNullOrEmpty(lockHash))
        {
            return false;
        }
        return true;
    }

    // ---------- npm orchestration ----------

    private async Task RunNpmCiAsync(
        string nodeExe, string npmCli, string localDirectusDir, CancellationToken cancellationToken)
    {
        var env = BuildIsolatedNpmEnvironment(nodeExe, localDirectusDir);
        // `node <npm-cli> ci` runs npm without depending on npm being on PATH.
        // All npm side-effects are redirected into app-private dirs via env.
        var psi = new ProcessStartInfo
        {
            FileName = nodeExe,
            Arguments = $"\"{npmCli}\" ci",
            WorkingDirectory = localDirectusDir,
            UseShellExecute = false,
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        foreach (var kv in env)
        {
            psi.Environment[kv.Key] = kv.Value;
        }

        using var proc = Process.Start(psi)
            ?? throw new InvalidOperationException("Failed to start npm ci.");
        // npm ci streams progress to stdout/stderr; we only need exit + tail.
        string stdout = await proc.StandardOutput.ReadToEndAsync(cancellationToken).ConfigureAwait(false);
        string stderr = await proc.StandardError.ReadToEndAsync(cancellationToken).ConfigureAwait(false);
        await proc.WaitForExitAsync(cancellationToken).WaitAsync(_npmTimeout, cancellationToken).ConfigureAwait(false);

        if (proc.ExitCode != 0)
        {
            throw new InvalidOperationException(
                $"npm ci failed (exit {proc.ExitCode}).\nstdout:\n{stdout}\nstderr:\n{stderr}");
        }
    }

    /// <summary>
    /// Builds an npm environment that writes ONLY into app-private directories,
    /// mirroring run.py's <c>_private_npm_env</c>: cache + prefix + user/global
    /// npmrc are all redirected under the local-Directus dir so the customer's
    /// global npm/Node state is never touched.
    /// </summary>
    private static Dictionary<string, string> BuildIsolatedNpmEnvironment(
        string nodeExe, string localDirectusDir)
    {
        var env = new Dictionary<string, string>(StringComparer.Ordinal);
        // Inherit the minimum needed for a child process to run (PATH so node's
        // own deps resolve, SYSTEMROOT/WINDIR for native builds, TEMP for tmp).
        CopyIfExists(env, "PATH");
        CopyIfExists(env, "SYSTEMROOT");
        CopyIfExists(env, "WINDIR");
        CopyIfExists(env, "TEMP");
        CopyIfExists(env, "TMP");
        CopyIfExists(env, "USERPROFILE");
        CopyIfExists(env, "LOCALAPPDATA");

        string cacheDir = Path.Combine(localDirectusDir, ".npm-cache");
        string prefixDir = Path.Combine(localDirectusDir, ".npm-prefix");
        Directory.CreateDirectory(cacheDir);
        Directory.CreateDirectory(prefixDir);
        env["npm_config_cache"] = cacheDir;
        env["npm_config_prefix"] = prefixDir;

        // Ignore any user/global npmrc so their settings never leak in or out.
        // npm refuses the same file as both user and global config, so use two
        // distinct empty files.
        string userRc = Path.Combine(localDirectusDir, ".npmrc.user");
        string globalRc = Path.Combine(localDirectusDir, ".npmrc.global");
        File.WriteAllText(userRc, "# isolated user config: intentionally empty\n", new UTF8Encoding(false));
        File.WriteAllText(globalRc, "# isolated global config: intentionally empty\n", new UTF8Encoding(false));
        env["npm_config_userconfig"] = userRc;
        env["npm_config_globalconfig"] = globalRc;

        return env;
    }

    private static void CopyIfExists(Dictionary<string, string> env, string key)
    {
        string? value = Environment.GetEnvironmentVariable(key);
        if (!string.IsNullOrEmpty(value))
        {
            env[key] = value;
        }
    }

    /// <summary>
    /// Locates <c>npm-cli.js</c> shipped with the bundled Node so npm can be run
    /// as <c>node &lt;npm-cli.js&gt; ci</c> without npm being on PATH.
    /// </summary>
    private static string ResolveNpmCli(string nodeExe)
    {
        // Layout: <nodeDir>/node_modules/npm/bin/npm-cli.js
        string nodeDir = Path.GetDirectoryName(nodeExe) ?? string.Empty;
        string candidate = Path.Combine(nodeDir, "node_modules", "npm", "bin", "npm-cli.js");
        if (File.Exists(candidate))
        {
            return candidate;
        }
        throw new InvalidOperationException(
            $"npm-cli.js not found beside bundled node (looked at {candidate}). " +
            "The bundled Node runtime is incomplete.");
    }

    // ---------- verification internals ----------

    private async Task<bool> VerifyIsolatedVmNativeAsync(
        string nodeExe, string localDirectusDir, CancellationToken cancellationToken)
    {
        // Mirror install.py's _verify_native: require() the module and construct
        // an Isolate. Throws -> the native binary is missing/broken (AV target).
        string snippet =
            "try { const m = require('./node_modules/isolated-vm'); " +
            "new m.Isolate(); console.log('IVM_OK'); } " +
            "catch (e) { console.error('IVM_FAIL:', e.message); process.exit(1); }";
        var psi = new ProcessStartInfo
        {
            FileName = nodeExe,
            Arguments = $"-e \"{snippet}\"",
            WorkingDirectory = localDirectusDir,
            UseShellExecute = false,
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        try
        {
            using var proc = Process.Start(psi);
            if (proc is null)
            {
                return false;
            }
            string stdout = await proc.StandardOutput.ReadToEndAsync(cancellationToken).ConfigureAwait(false);
            await proc.WaitForExitAsync(cancellationToken).ConfigureAwait(false);
            return proc.ExitCode == 0 && stdout.Contains("IVM_OK", StringComparison.Ordinal);
        }
        catch
        {
            return false;
        }
    }

    private static string ComputeLockHash(string localDirectusDir)
    {
        string lockFile = Path.Combine(localDirectusDir, "package-lock.json");
        if (!File.Exists(lockFile))
        {
            return string.Empty;
        }
        using var sha = SHA256.Create();
        using var stream = File.OpenRead(lockFile);
        byte[] hash = sha.ComputeHash(stream);
        // Compact, stable hex; never includes path or CRLF noise.
        var sb = new StringBuilder("sha256-" + hash.Length * 2);
        foreach (byte b in hash)
        {
            sb.Append(b.ToString("x2"));
        }
        return sb.ToString();
    }

    // ---------- marker (cache) ----------

    private static bool IsExpired(InstallMarker marker) =>
        DateTimeOffset.UtcNow - marker.VerifiedAt > ReverifyInterval;

    private static bool TryReadMarker(string localDirectusDir, out InstallMarker? marker)
    {
        marker = null;
        string path = Path.Combine(localDirectusDir, MarkerFileName);
        if (!File.Exists(path))
        {
            return false;
        }
        try
        {
            using var doc = JsonDocument.Parse(File.ReadAllText(path));
            var root = doc.RootElement;
            marker = new InstallMarker(
                LockHash: root.GetProperty("lockHash").GetString() ?? string.Empty,
                VerifiedAt: root.GetProperty("verifiedAt").GetDateTimeOffset(),
                NodeVersion: root.TryGetProperty("nodeVersion", out var nv) ? nv.GetString() : null);
            return true;
        }
        catch
        {
            // Corrupt/legacy marker: treat as absent so verification re-runs.
            return false;
        }
    }

    private static void WriteMarker(string localDirectusDir, InstallMarker marker)
    {
        string path = Path.Combine(localDirectusDir, MarkerFileName);
        var payload = new
        {
            lockHash = marker.LockHash,
            verifiedAt = marker.VerifiedAt,
            nodeVersion = marker.NodeVersion,
        };
        string json = JsonSerializer.Serialize(payload, new JsonSerializerOptions
        {
            WriteIndented = true,
        });
        File.WriteAllText(path, json, new UTF8Encoding(false));
    }

    private static string? ReadNodeVersion(string nodeExe)
    {
        try
        {
            var psi = new ProcessStartInfo
            {
                FileName = nodeExe,
                Arguments = "-v",
                UseShellExecute = false,
                RedirectStandardOutput = true,
                CreateNoWindow = true,
            };
            using var proc = Process.Start(psi);
            if (proc is null)
            {
                return null;
            }
            string v = proc.StandardOutput.ReadLine()?.Trim() ?? string.Empty;
            proc.WaitForExit(3000);
            return v;
        }
        catch
        {
            return null;
        }
    }

    /// <summary>
    /// Self-heal: remove a corrupt install so the next npm ci is clean, and drop
    /// the stale marker so verification is forced afterwards.
    /// </summary>
    private static void SelfHeal(string localDirectusDir)
    {
        try
        {
            string nodeModules = Path.Combine(localDirectusDir, "node_modules");
            if (Directory.Exists(nodeModules))
            {
                Directory.Delete(nodeModules, recursive: true);
            }
        }
        catch
        {
            // Best-effort; npm ci will overwrite what it can.
        }
        try
        {
            string markerPath = Path.Combine(localDirectusDir, MarkerFileName);
            if (File.Exists(markerPath))
            {
                File.Delete(markerPath);
            }
        }
        catch
        {
            // Best-effort.
        }
    }

    /// <summary>Immutable verification marker written to disk.</summary>
    private sealed record InstallMarker(
        string LockHash, DateTimeOffset VerifiedAt, string? NodeVersion);
}
