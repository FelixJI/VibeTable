using System.IO;

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
}
