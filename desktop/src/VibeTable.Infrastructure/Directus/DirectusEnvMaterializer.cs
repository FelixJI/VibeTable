using System;
using System.Collections.Generic;
using System.IO;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Materializes the local Directus <c>.env</c> from <c>.env.template</c> and
/// owns the runtime secrets/port decisions previously embedded in
/// <c>scripts/local_directus/run.py</c>. Pure file + value work — no processes.
/// </summary>
/// <remarks>
/// <para>
/// Mirrors <c>run.py</c>'s <c>materialize_env</c> + <c>pick_port</c> exactly so
/// the on-disk result is byte-compatible:
/// </para>
/// <list type="bullet">
/// <item>First run: generate random KEY/SECRET/ADMIN_PASSWORD (URL-safe base64
/// of 32 random bytes, matching <c>secrets.token_urlsafe(32)</c>).</item>
/// <item>Subsequent runs: preserve already-generated secrets from the existing
/// <c>.env</c> so the SQLite DB / JWT signing key never rotates.</item>
/// <item>Bootstrap credentials supplied by the host (first-run setup dialog)
/// override ADMIN_EMAIL/ADMIN_PASSWORD only before the <c>.bootstrapped</c>
/// marker exists; afterwards they are ignored.</item>
/// <item>Port: prefer the configured PORT, auto-evade to a free port on
/// conflict, and persist the resolved port back into <c>.env</c>.</item>
/// </list>
/// </remarks>
public static class DirectusEnvMaterializer
{
    /// <summary>
    /// Default port in the IANA ephemeral range (49152+), off the well-known
    /// 8055. Avoids clashes with registered services.
    /// </summary>
    public const int DefaultPort = 49152;

    private const string GeneratePlaceholder = "__GENERATE__";
    private static readonly string[] GeneratedKeys =
    {
        "KEY",
        "SECRET",
        "ADMIN_PASSWORD",
        "VIBETABLE_HISTORY_PROOF_SECRET",
    };

    /// <summary>
    /// Probe range for port-conflict evasion. Exposed for tests so the
    /// "high ephemeral range" invariant can be asserted.
    /// </summary>
    public const int PortProbeRangeStart = DefaultPort;
    public const int PortProbeRangeEnd = DefaultPort + 50;

    /// <summary>
    /// Creates/refreshes <c>.env</c> in <paramref name="directory"/> and returns
    /// the resolved key/value map. Idempotent across runs (preserves secrets).
    /// </summary>
    /// <param name="bootstrapEmail">Optional first-run admin email from the host
    /// setup dialog; applied only before the DB is bootstrapped.</param>
    /// <param name="bootstrapPassword">Optional first-run admin password; same
    /// lifecycle as <paramref name="bootstrapEmail"/>.</param>
    /// <param name="isBootstrapped">Whether the <c>.bootstrapped</c> marker is
    /// present (bootstrap creds are ignored once true).</param>
    public static Dictionary<string, string> Materialize(
        string directory,
        string? bootstrapEmail = null,
        string? bootstrapPassword = null,
        bool isBootstrapped = false)
    {
        string envFile = Path.Combine(directory, ".env");
        var values = ParseTemplate(directory);

        // First-run secret generation for the placeholder keys.
        foreach (string key in GeneratedKeys)
        {
            if (values.TryGetValue(key, out string? v) && (v == GeneratePlaceholder || string.IsNullOrEmpty(v)))
            {
                values[key] = GenerateSecret();
            }
        }

        // Preserve previously generated secrets so a restart never rotates the
        // signing key / DB credentials.
        if (File.Exists(envFile))
        {
            var existing = ParseEnvFile(File.ReadAllText(envFile));
            foreach (string key in values.Keys)
            {
                if (existing.TryGetValue(key, out string? prev) && prev != GeneratePlaceholder)
                {
                    values[key] = prev;
                }
            }
            // Merge any keys present in .env but not the template (forward-compat).
            foreach (var kv in existing)
            {
                values.TryAdd(kv.Key, kv.Value);
            }
        }

        // Apply bootstrap credentials only on first boot.
        if (!isBootstrapped)
        {
            if (!string.IsNullOrWhiteSpace(bootstrapEmail))
            {
                values["ADMIN_EMAIL"] = bootstrapEmail!;
            }
            if (!string.IsNullOrEmpty(bootstrapPassword))
            {
                values["ADMIN_PASSWORD"] = bootstrapPassword!;
            }
        }

        EnsureSqliteDatabaseDirectory(directory, values);

        // Host-owned security defaults. These close the default 0.0.0.0
        // exposure (HOST) and make the injected session cookie usable on a
        // loopback http origin (SESSION_COOKIE_*). Applied on every
        // materialization; not user-overridable in the supported workflow.
        values["HOST"] = "127.0.0.1";
        values["SESSION_COOKIE_TTL"] = "7d";
        values["SESSION_COOKIE_SAME_SITE"] = "lax";

        WriteEnv(envFile, values);
        return values;
    }

    /// <summary>
    /// Ensures the parent directory for the configured app-private SQLite
    /// database exists before <c>directus bootstrap</c> opens the file.
    /// A fresh first run has no <c>data/</c> directory yet.
    /// </summary>
    private static void EnsureSqliteDatabaseDirectory(
        string runtimeDirectory,
        IReadOnlyDictionary<string, string> values)
    {
        if (!values.TryGetValue("DB_CLIENT", out string? client)
            || !string.Equals(client, "sqlite3", StringComparison.OrdinalIgnoreCase)
            || !values.TryGetValue("DB_FILENAME", out string? filename)
            || string.IsNullOrWhiteSpace(filename)
            || string.Equals(filename, ":memory:", StringComparison.Ordinal))
        {
            return;
        }

        string runtimeRoot = Path.GetFullPath(runtimeDirectory);
        string databasePath = Path.GetFullPath(
            Path.IsPathRooted(filename)
                ? filename
                : Path.Combine(runtimeRoot, filename));
        string runtimePrefix = runtimeRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
        if (!databasePath.StartsWith(runtimePrefix, StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException(
                $"SQLite DB_FILENAME must remain inside the local Directus runtime: {filename}");
        }

        string? parent = Path.GetDirectoryName(databasePath);
        if (!string.IsNullOrEmpty(parent))
        {
            Directory.CreateDirectory(parent);
        }
    }

    /// <summary>
    /// Reads the administrator credentials persisted by a previous local
    /// bootstrap. Retained as a defensive utility: the desktop host no longer
    /// consumes it (an interrupted first run is now reset back to fresh via
    /// <see cref="DirectusFirstRunState.ResetUncompletedBootstrap"/> rather
    /// than resumed), but the credential-read path is kept for diagnostics and
    /// future tooling. The secret remains in memory and is never logged.
    /// </summary>
    public static bool TryReadBootstrapCredentials(
        string directory,
        out string email,
        out string password)
    {
        email = string.Empty;
        password = string.Empty;
        string envFile = Path.Combine(directory, ".env");
        if (!File.Exists(envFile))
        {
            return false;
        }

        var values = ParseEnvFile(File.ReadAllText(envFile));
        if (!values.TryGetValue("ADMIN_EMAIL", out string? savedEmail)
            || string.IsNullOrWhiteSpace(savedEmail)
            || !values.TryGetValue("ADMIN_PASSWORD", out string? savedPassword)
            || string.IsNullOrEmpty(savedPassword)
            || string.Equals(savedPassword, GeneratePlaceholder, StringComparison.Ordinal))
        {
            return false;
        }

        email = savedEmail;
        password = savedPassword;
        return true;
    }

    /// <summary>
    /// Returns a free TCP port, preferring <paramref name="preferred"/> and
    /// auto-evading on conflict (mirrors run.py's <c>pick_port</c>). Throws if
    /// the whole probe range is occupied.
    /// </summary>
    public static int PickFreePort(int preferred)
    {
        if (!IsPortInUse(preferred))
        {
            return preferred;
        }
        for (int port = PortProbeRangeStart; port < PortProbeRangeEnd; port++)
        {
            if (port != preferred && !IsPortInUse(port))
            {
                return port;
            }
        }
        throw new InvalidOperationException(
            $"No free port found in range {PortProbeRangeStart}-{PortProbeRangeEnd - 1}.");
    }

    /// <summary>Parse a <c>.env.template</c>/<c>.env</c> file into a map.</summary>
    public static Dictionary<string, string> ParseEnvFile(string text)
    {
        var values = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (string raw in text.Split('\n', StringSplitOptions.RemoveEmptyEntries))
        {
            string line = raw.Trim();
            if (line.Length == 0 || line.StartsWith("#", StringComparison.Ordinal) || !line.Contains('='))
            {
                continue;
            }
            int eq = line.IndexOf('=');
            values[line[..eq].Trim()] = line[(eq + 1)..].Trim();
        }
        return values;
    }

    /// <summary>Parse <c>.env.template</c> in <paramref name="directory"/>.</summary>
    public static Dictionary<string, string> ParseTemplate(string directory)
    {
        string template = Path.Combine(directory, ".env.template");
        return ParseEnvFile(File.ReadAllText(template));
    }

    /// <summary>Write the map back to <paramref name="envFile"/> (overwrite).</summary>
    public static void WriteEnv(string envFile, Dictionary<string, string> values)
    {
        var sb = new StringBuilder();
        sb.AppendLine("# Auto-materialized by DirectusEnvMaterializer. Edit by hand if needed.");
        sb.AppendLine("# This file is gitignored; never commit real KEY/SECRET/passwords.");
        sb.AppendLine();
        foreach (var kv in values)
        {
            sb.Append(kv.Key).Append('=').AppendLine(kv.Value);
        }
        File.WriteAllText(envFile, sb.ToString(), new UTF8Encoding(false));
    }

    /// <summary>URL-safe base64 of 32 random bytes — matches secrets.token_urlsafe(32).</summary>
    private static string GenerateSecret()
    {
        byte[] bytes = RandomNumberGenerator.GetBytes(32);
        return Convert.ToBase64String(bytes)
            .Replace('+', '-')
            .Replace('/', '_')
            .TrimEnd('=');
    }

    private static bool IsPortInUse(int port)
    {
        try
        {
            using var sock = new Socket(AddressFamily.InterNetwork, SocketType.Stream, ProtocolType.Tcp);
            sock.Bind(new System.Net.IPEndPoint(System.Net.IPAddress.Loopback, port));
            return false;
        }
        catch (SocketException)
        {
            return true;
        }
    }
}
