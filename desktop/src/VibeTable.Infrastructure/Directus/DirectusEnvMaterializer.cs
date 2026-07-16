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
    /// <summary>Default port (matches <c>.env.template</c> PORT and run.py).</summary>
    public const int DefaultPort = 8055;

    private const string GeneratePlaceholder = "__GENERATE__";
    private static readonly string[] GeneratedKeys = { "KEY", "SECRET", "ADMIN_PASSWORD" };
    private const int PortProbeRangeStart = DefaultPort;
    private const int PortProbeRangeEnd = DefaultPort + 100;

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

        WriteEnv(envFile, values);
        return values;
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
