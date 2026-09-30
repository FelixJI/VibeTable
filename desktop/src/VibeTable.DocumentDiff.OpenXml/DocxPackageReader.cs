using System.IO.Compression;
using System.IO.Packaging;
using System.Net.Mime;
using System.Xml;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed class DocxPackageReader : IDisposable
{
    private const int MaxXmlNodes = 250_000;
    private readonly Stream _source;
    private readonly Stream _seekable;
    private readonly ZipArchive _archive;
    private readonly ExpandedByteBudget _budget;
    private readonly Dictionary<string, ZipArchiveEntry> _parts;
    private bool _disposed;
    private int _remainingXmlNodes = MaxXmlNodes;

    private DocxPackageReader(Stream source, Stream seekable, ZipArchive archive,
        ExpandedByteBudget budget, Dictionary<string, ZipArchiveEntry> parts)
    {
        _source = source;
        _seekable = seekable;
        _archive = archive;
        _budget = budget;
        _parts = parts;
        PartNames = Array.AsReadOnly(parts.Values.Select(entry => entry.FullName)
            .Order(StringComparer.Ordinal).ToArray());
    }

    public IReadOnlyList<string> PartNames { get; }

    public static async Task<DocxPackageReader> OpenAsync(DocumentContentSource content,
        CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(content);
        cancellationToken.ThrowIfCancellationRequested();
        Stream source = await content.OpenReadAsync(cancellationToken).ConfigureAwait(false);
        Stream? seekable = null;
        ZipArchive? archive = null;
        try
        {
            seekable = await EnsureSeekableAsync(content, source, cancellationToken).ConfigureAwait(false);
            archive = new ZipArchive(seekable, ZipArchiveMode.Read, leaveOpen: true);
            if (archive.Entries.Count > OpenXmlExtractionLimits.MaxPackageEntries)
                throw new DiffBudgetExceededException();
            // DOCX parts are classified by content type, not by filename suffix.
            // XML byte limits remain enforced when each selected part is read.
            var budget = new ExpandedByteBudget();
            var parts = new Dictionary<string, ZipArchiveEntry>(StringComparer.OrdinalIgnoreCase);
            foreach (ZipArchiveEntry entry in archive.Entries)
            {
                cancellationToken.ThrowIfCancellationRequested();
                string name = entry.FullName;
                bool directory = name.EndsWith('/');
                string decoded = Uri.UnescapeDataString(directory ? name[..^1] : name);
                if (decoded.Length == 0 || decoded.Contains('\\') || decoded.Contains(':') ||
                    decoded.Contains('\0') || decoded.Split('/').Any(segment => segment is "" or "." or "..") ||
                    ((entry.ExternalAttributes >> 16) & 0xF000) == 0xA000)
                    throw new InvalidDataException("Invalid package part name or link entry.");
                if (directory)
                {
                    if (entry.Length != 0)
                        throw new InvalidDataException("Package directory contains data.");
                    continue;
                }
                if (!parts.TryAdd(DocxRelationships.PartKey(name), entry))
                    throw new InvalidDataException("Ambiguous duplicate package part.");
            }
            if (!parts.ContainsKey("[Content_Types].xml") || !parts.ContainsKey(DocxRelationships.PartKey("_rels/.rels")))
                throw new InvalidDataException("Missing package metadata.");
            return new DocxPackageReader(source, seekable, archive, budget, parts);
        }
        catch
        {
            archive?.Dispose();
            if (seekable is not null)
                await seekable.DisposeAsync().ConfigureAwait(false);
            await source.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    }

    public XDocument ReadXmlPart(string name, CancellationToken cancellationToken = default)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        cancellationToken.ThrowIfCancellationRequested();
        ArgumentException.ThrowIfNullOrWhiteSpace(name);
        string key = DocxRelationships.PartKey(name);
        // OPC content types, not ZIP filename suffixes, identify XML parts.
        // The caller selects a part; secure parsing and all read budgets still apply.
        if (!_parts.TryGetValue(key, out ZipArchiveEntry? entry))
            throw new InvalidDataException("Missing package part.");
        using Stream bounded = OpenBoundedEntry(entry, _budget);
        using var bytes = new MemoryStream();
        var buffer = new byte[64 * 1024];
        int read;
        while ((read = bounded.Read(buffer)) != 0)
        {
            cancellationToken.ThrowIfCancellationRequested();
            bytes.Write(buffer, 0, read);
        }
        bytes.Position = 0;
        // Preflight before allocating the DOM. This second in-memory XML pass
        // avoids an unbounded object graph without implementing a custom parser.
        ValidateXml(bytes, ref _remainingXmlNodes, cancellationToken);
        cancellationToken.ThrowIfCancellationRequested();
        bytes.Position = 0;
        using var documentReader = XmlReader.Create(bytes, SecureXmlSettings());
        XDocument document = XDocument.Load(documentReader, LoadOptions.PreserveWhitespace);
        cancellationToken.ThrowIfCancellationRequested();
        return document;
    }

    internal bool ContainsPart(string name)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        return _parts.ContainsKey(DocxRelationships.PartKey(name));
    }

    internal void ValidateAllParts(CancellationToken cancellationToken, Action<string, string>? validateElement = null)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_remainingXmlNodes != MaxXmlNodes)
            throw new InvalidOperationException("Whole-package validation requires a fresh reader.");
        DocxContentTypes types = DocxContentTypes.Read(this, cancellationToken);
        var packageBudget = new ExpandedByteBudget(DocxPackagePreflight.MaxExpandedPackageBytes);
        // Metadata was parsed once above, including EOF/length and XML budgets.
        packageBudget.Consume(checked((int)_parts["[Content_Types].xml"].Length));
        var buffer = new byte[64 * 1024];
        foreach (ZipArchiveEntry entry in _parts.Values)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (entry.FullName == "[Content_Types].xml")
                continue;
            string key = DocxRelationships.PartKey(entry.FullName);
            string mediaType = new ContentType(types.Get(entry.FullName)).MediaType;
            bool xml = mediaType.Equals("application/xml", StringComparison.OrdinalIgnoreCase) ||
                mediaType.Equals("text/xml", StringComparison.OrdinalIgnoreCase) ||
                mediaType.EndsWith("+xml", StringComparison.OrdinalIgnoreCase);
            bool relationshipsPart = PackUriHelper.IsRelationshipPartUri(new Uri(key, UriKind.Relative));
            if (relationshipsPart)
            {
                if (!mediaType.Equals("application/vnd.openxmlformats-package.relationships+xml", StringComparison.OrdinalIgnoreCase))
                    throw new InvalidDataException("Invalid relationships content type.");
                Uri source = PackUriHelper.GetSourcePartUriFromRelationshipPartUri(new Uri(key, UriKind.Relative));
                if (source.OriginalString != "/")
                    ResolvePartName(source.OriginalString.TrimStart('/'));
                var relationships = DocxRelationships.Parse(this, source,
                    ReadXmlPart(entry.FullName, cancellationToken).Root, cancellationToken);
                packageBudget.Consume(checked((int)entry.Length));
                foreach (DocxRelationship relationship in relationships)
                    ValidateWordXmlRelationship(relationship, types);
                continue;
            }
            long partLimit = xml ? OpenXmlExtractionLimits.MaxXmlPartBytes : DocxPackagePreflight.MaxBinaryPartBytes;
            if (entry.Length > partLimit)
                throw new DiffBudgetExceededException();
            using var bounded = new BudgetedEntryStream(entry.Open(), packageBudget, partLimit, entry.Length);
            if (xml)
            {
                using var xmlBounded = new BudgetedEntryStream(bounded, _budget, expectedLength: entry.Length);
                ValidateXml(xmlBounded, ref _remainingXmlNodes, cancellationToken, validateElement);
            }
            else
            {
                while (bounded.Read(buffer) != 0)
                    cancellationToken.ThrowIfCancellationRequested();
            }
        }
    }

    private static void ValidateWordXmlRelationship(DocxRelationship relationship, DocxContentTypes types)
    {
        const string prefix = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/";
        if (!relationship.Type.StartsWith(prefix, StringComparison.Ordinal))
            return;
        string kind = relationship.Type[prefix.Length..];
        string? contentKind = kind switch
        {
            "officeDocument" => "document.main",
            "header" or "footer" or "footnotes" or "endnotes" or "styles" or "settings" => kind,
            _ => null,
        };
        if (contentKind is null)
            return;
        if (relationship.IsExternal || relationship.PartName is null || relationship.Fragment is not null ||
            !new ContentType(types.Get(relationship.PartName)).MediaType.Equals(
                "application/vnd.openxmlformats-officedocument.wordprocessingml." + contentKind + "+xml",
                StringComparison.OrdinalIgnoreCase))
            throw new InvalidDataException("Invalid Word XML relationship content type.");
    }

    internal string ResolvePartName(string name)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        return _parts.TryGetValue(DocxRelationships.PartKey(name), out ZipArchiveEntry? entry)
            ? entry.FullName : throw new InvalidDataException("Missing package part.");
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        try { _archive.Dispose(); }
        finally
        {
            try { _seekable.Dispose(); }
            finally { _source.Dispose(); }
        }
    }
}
