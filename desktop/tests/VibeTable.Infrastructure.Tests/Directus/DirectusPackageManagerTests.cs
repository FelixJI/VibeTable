using System;
using System.IO;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

/// <summary>
/// Tests for <see cref="DirectusPackageManager"/> covering the deterministic
/// file-system logic: marker caching (skip / expiry), and structural
/// verification of a partial install. The npm-ci orchestration and the native
/// <c>isolated-vm</c> load probe run real subprocesses; the native probe is
/// exercised against the repo's bundled Node (checked in, always present),
/// while npm ci itself is covered by the integration e2e path rather than here.
/// </summary>
[TestClass]
public sealed class DirectusPackageManagerTests
{
    [TestMethod]
    public async Task VerifyAsync_ReturnsFalse_WhenDirectusPackageAbsent()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, "package-lock.json"), "{}");
            var manager = new DirectusPackageManager();
            // No node_modules/directus at all.
            bool ok = manager.VerifyAsync(BundledNodePath(), dir, expectedLockHash: null, CancellationToken.None)
                .GetAwaiter().GetResult();
            Assert.IsFalse(ok, "verify must fail when node_modules/directus is missing");
        });
        await Task.CompletedTask;
    }

    [TestMethod]
    public async Task VerifyAsync_ReturnsFalse_WhenLockfileAbsent()
    {
        WithTemporaryDirectory(dir =>
        {
            // directus dir exists but no package-lock.json -> empty lock hash -> false.
            Directory.CreateDirectory(Path.Combine(dir, "node_modules", "directus"));
            var manager = new DirectusPackageManager();
            bool ok = manager.VerifyAsync(BundledNodePath(), dir, expectedLockHash: null, CancellationToken.None)
                .GetAwaiter().GetResult();
            Assert.IsFalse(ok);
        });
        await Task.CompletedTask;
    }

    /// <summary>
    /// A fresh marker with a matching lockfile hash must short-circuit
    /// <see cref="DirectusPackageManager.EnsureInstalledAsync"/> so a warm start
    /// pays neither npm nor the native probe. We assert the marker is honoured
    /// by observing EnsureInstalled returns without invoking npm (no node_modules
    /// is created) when the marker is fresh and matches.
    /// </summary>
    [TestMethod]
    public async Task EnsureInstalled_SkipsNpm_WhenMarkerFreshAndMatches()
    {
        WithTemporaryDirectory(dir =>
        {
            string lockContent = "{\"name\":\"x\"}";
            File.WriteAllText(Path.Combine(dir, "package-lock.json"), lockContent);
            // A pre-existing marker that matches the current lockfile hash and is
            // brand-new (well within the 7-day reverify window).
            string hash = ComputeLockHashFor(dir);
            WriteMarker(dir, new { lockHash = hash, verifiedAt = DateTimeOffset.UtcNow, nodeVersion = "v24.18.0" });

            var manager = new DirectusPackageManager();
            // Should return without throwing AND without creating node_modules
            // (which only npm ci would do).
            manager.EnsureInstalledAsync(BundledNodePath(), dir, CancellationToken.None)
                .GetAwaiter().GetResult();

            Assert.IsFalse(Directory.Exists(Path.Combine(dir, "node_modules")),
                "a fresh matching marker must short-circuit before npm runs");
        });
        await Task.CompletedTask;
    }

    /// <summary>
    /// An expired marker (older than the 7-day window) must NOT short-circuit:
    /// EnsureInstalled proceeds to install. We assert it does NOT silently
    /// return — it either installs or throws, but it does not treat the stale
    /// marker as authoritative. Because npm ci is unavailable in the unit
    /// fixture, we expect it to throw here; the point is it did not skip.
    /// </summary>
    [TestMethod]
    public async Task EnsureInstalled_DoesNotSkip_WhenMarkerExpired()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, "package-lock.json"), "{\"name\":\"x\"}");
            string hash = ComputeLockHashFor(dir);
            WriteMarker(dir, new
            {
                lockHash = hash,
                verifiedAt = DateTimeOffset.UtcNow - TimeSpan.FromDays(30), // expired
                nodeVersion = "v24.18.0",
            });

            var manager = new DirectusPackageManager(npmTimeout: TimeSpan.FromSeconds(5));
            // Expired marker -> install is attempted -> npm ci fails (no real
            // package set up) -> throws. Asserting throw proves it did NOT skip.
            Assert.Throws<InvalidOperationException>(() =>
                manager.EnsureInstalledAsync(BundledNodePath(), dir, CancellationToken.None)
                    .GetAwaiter().GetResult());
        });
        await Task.CompletedTask;
    }

    /// <summary>
    /// A marker whose lockfile hash differs from the current package-lock.json
    /// must be treated as stale (the dependency graph changed): EnsureInstalled
    /// re-runs install instead of trusting the marker.
    /// </summary>
    [TestMethod]
    public async Task EnsureInstalled_DoesNotSkip_WhenLockHashChanged()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, "package-lock.json"), "{\"name\":\"new\"}");
            // Marker claims an old hash that no longer matches.
            WriteMarker(dir, new
            {
                lockHash = "sha256-stale-does-not-match",
                verifiedAt = DateTimeOffset.UtcNow,
                nodeVersion = "v24.18.0",
            });

            var manager = new DirectusPackageManager(npmTimeout: TimeSpan.FromSeconds(5));
            Assert.Throws<InvalidOperationException>(() =>
                manager.EnsureInstalledAsync(BundledNodePath(), dir, CancellationToken.None)
                    .GetAwaiter().GetResult());
        });
        await Task.CompletedTask;
    }

    // ---------- helpers ----------

    /// <summary>Path to the repo's bundled node.exe (runtime/node/node.exe).</summary>
    private static string BundledNodePath()
    {
        // The test assembly runs from desktop/tests/.../bin/Debug/net10.0-windows.
        // Walk up to the repo root and resolve runtime/node/node.exe.
        string dir = AppContext.BaseDirectory;
        for (int i = 0; i < 8; i++)
        {
            string candidate = Path.Combine(dir, "runtime", "node", "node.exe");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            dir = Path.GetFullPath(Path.Combine(dir, ".."));
        }
        // Fallback: assume node is on PATH (dev machines without the bundled dir).
        return "node";
    }

    private static string ComputeLockHashFor(string dir)
    {
        // Mirror the manager's internal hashing exactly so the marker matches.
        string lockFile = Path.Combine(dir, "package-lock.json");
        using var sha = System.Security.Cryptography.SHA256.Create();
        using var stream = File.OpenRead(lockFile);
        byte[] hash = sha.ComputeHash(stream);
        var sb = new System.Text.StringBuilder("sha256-" + hash.Length * 2);
        foreach (byte b in hash)
        {
            sb.Append(b.ToString("x2"));
        }
        return sb.ToString();
    }

    private static void WriteMarker(string dir, object payload)
    {
        string json = JsonSerializer.Serialize(payload, new JsonSerializerOptions { WriteIndented = true });
        File.WriteAllText(Path.Combine(dir, DirectusPackageManager.MarkerFileName), json);
    }

    private static void WithTemporaryDirectory(Action<string> body)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-pm-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(root);
            body(root);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort */ }
        }
    }
}
