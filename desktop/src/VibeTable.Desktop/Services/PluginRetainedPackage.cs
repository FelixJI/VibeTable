using System.Buffers.Binary;
using System.IO;
using System.IO.Compression;
using System.Security.Cryptography;
using System.Text;

namespace VibeTable.Desktop.Services;

internal static class PluginRetainedPackage
{
    // Same retained-cache filename as LocalPluginPackageLifecycle. The digest
    // comes from the verified package identity; this does not hash local data.
    internal static string? PathFor(string? cacheRoot, string packageHash)
    {
        if (string.IsNullOrWhiteSpace(cacheRoot) || packageHash.Length != 71
            || !packageHash.StartsWith("sha256:", StringComparison.Ordinal)
            || packageHash.AsSpan(7).ContainsAnyExcept("0123456789abcdef")) return null;
        byte[] digest = Convert.FromHexString(packageHash.AsSpan(7));
        const string alphabet = "abcdefghijklmnopqrstuvwxyz234567";
        Span<char> encoded = stackalloc char[52];
        int buffer = 0, bits = 0, count = 0;
        foreach (byte value in digest)
        {
            buffer = (buffer << 8) | value;
            bits += 8;
            while (bits >= 5) { bits -= 5; encoded[count++] = alphabet[(buffer >> bits) & 31]; }
        }
        if (bits != 0) encoded[count++] = alphabet[(buffer << (5 - bits)) & 31];
        return Path.Combine(Path.GetFullPath(cacheRoot), new string(encoded[..count]) + ".vtplugin");
    }

    // The install authority hashes normalized ZIP members, including integrity.json,
    // rather than ZIP bytes (backend.infrastructure.plugin_package._normalized_package_digest).
    // Keep this read handle open through copying so Windows denies cache replacement.
    internal static async Task VerifyAsync(FileStream source, string packageHash, CancellationToken token)
    {
        if (source.Length > 64 * 1024 * 1024) throw new IOException("Package exceeds its installed size limit.");
        // Python zipfile uses CP437 when the entry does not declare UTF-8.
        using var archive = new ZipArchive(source, ZipArchiveMode.Read, leaveOpen: true,
            entryNameEncoding: CodePagesEncodingProvider.Instance.GetEncoding(437));
        var members = new List<(ZipArchiveEntry Entry, byte[] Name)>();
        var names = new HashSet<string>(StringComparer.Ordinal);
        long total = 0;
        foreach (ZipArchiveEntry entry in archive.Entries)
        {
            token.ThrowIfCancellationRequested();
            if (entry.FullName.EndsWith('/')) continue;
            string path = entry.FullName.Replace('\\', '/').Normalize(NormalizationForm.FormC);
            string[] parts = path.Split('/');
            if (path.StartsWith('/') || path.Contains('\0') || parts.Contains("..")
                || (path.Length >= 2 && char.IsAsciiLetter(path[0]) && path[1] == ':')
                || ((entry.ExternalAttributes >> 16) & 0xF000) == 0xA000)
                throw new IOException("Invalid retained package member.");
            string name = string.Join('/', parts.Where(part => part is not ("" or ".")));
            if (name.Length == 0 || !names.Add(name) || members.Count >= 2048
                || entry.Length > 32 * 1024 * 1024 || (total += entry.Length) > 256 * 1024 * 1024)
                throw new IOException("Invalid retained package member or size.");
            members.Add((entry, Encoding.UTF8.GetBytes(name)));
        }
        using var digest = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        byte[] length = new byte[8];
        byte[] buffer = new byte[64 * 1024];
        // UTF-8 lexical order matches the authority's Unicode code-point order.
        foreach (var member in members.OrderBy(member => member.Name,
            Comparer<byte[]>.Create((left, right) => left.AsSpan().SequenceCompareTo(right))))
        {
            BinaryPrimitives.WriteUInt64BigEndian(length, (ulong)member.Name.Length);
            digest.AppendData(length);
            digest.AppendData(member.Name);
            BinaryPrimitives.WriteUInt64BigEndian(length, (ulong)member.Entry.Length);
            digest.AppendData(length);
            await using Stream content = member.Entry.Open();
            long readTotal = 0;
            int count;
            while ((count = await content.ReadAsync(buffer, token).ConfigureAwait(false)) != 0)
            {
                if ((readTotal += count) > member.Entry.Length) throw new IOException("Invalid retained package size.");
                digest.AppendData(buffer, 0, count);
            }
            if (readTotal != member.Entry.Length) throw new IOException("Incomplete retained package member.");
        }
        string actual = "sha256:" + Convert.ToHexString(digest.GetHashAndReset()).ToLowerInvariant();
        if (actual != packageHash) throw new IOException("Retained package differs from its installed identity.");
    }
}
