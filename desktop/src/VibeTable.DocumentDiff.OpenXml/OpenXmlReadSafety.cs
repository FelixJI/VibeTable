using System.IO.Compression;
using System.Xml;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

internal static class OpenXmlReadSafety
{
    internal static XmlReaderSettings SecureXmlSettings()
    {
        return new XmlReaderSettings
        {
            DtdProcessing = DtdProcessing.Prohibit,
            XmlResolver = null,
        };
    }

    internal static ExpandedByteBudget ValidateArchive(ZipArchive archive)
    {
        if (archive.Entries.Count > OpenXmlExtractionLimits.MaxPackageEntries)
        {
            throw new DiffBudgetExceededException();
        }
        long declaredXmlBytes = 0;
        foreach (var entry in archive.Entries)
        {
            if (!entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            {
                continue;
            }
            if (entry.Length > OpenXmlExtractionLimits.MaxXmlPartBytes)
            {
                throw new DiffBudgetExceededException();
            }
            declaredXmlBytes = checked(declaredXmlBytes + entry.Length);
            if (declaredXmlBytes > OpenXmlExtractionLimits.MaxExpandedXmlBytes)
            {
                throw new DiffBudgetExceededException();
            }
        }
        return new ExpandedByteBudget();
    }

    internal static Stream OpenBoundedEntry(
        ZipArchiveEntry entry,
        ExpandedByteBudget expandedBudget)
    {
        if (entry.Length > OpenXmlExtractionLimits.MaxXmlPartBytes)
        {
            throw new DiffBudgetExceededException();
        }
        return new BudgetedEntryStream(entry.Open(), expandedBudget, expectedLength: entry.Length);
    }

    internal static async ValueTask<Stream> EnsureSeekableAsync(
        DocumentContentSource content,
        Stream source,
        CancellationToken cancellationToken)
    {
        if (content.Length is > OpenXmlExtractionLimits.MaxNonSeekablePackageBytes)
        {
            throw new DiffBudgetExceededException();
        }
        if (source.CanSeek)
        {
            if (source.Length > OpenXmlExtractionLimits.MaxNonSeekablePackageBytes)
            {
                throw new DiffBudgetExceededException();
            }
            source.Position = 0;
            return new NonOwningStream(source);
        }

        var copy = new MemoryStream();
        try
        {
            var buffer = new byte[64 * 1024];
            long total = 0;
            while (true)
            {
                cancellationToken.ThrowIfCancellationRequested();
                var read = await source.ReadAsync(buffer, cancellationToken).ConfigureAwait(false);
                if (read == 0)
                {
                    break;
                }
                total += read;
                if (total > OpenXmlExtractionLimits.MaxNonSeekablePackageBytes)
                {
                    throw new DiffBudgetExceededException();
                }
                await copy.WriteAsync(buffer.AsMemory(0, read), cancellationToken)
                    .ConfigureAwait(false);
            }
            copy.Position = 0;
            return copy;
        }
        catch
        {
            copy.Dispose();
            throw;
        }
    }

    internal sealed class NonOwningStream(Stream inner) : Stream
    {
        public override bool CanRead => inner.CanRead;

        public override bool CanSeek => inner.CanSeek;

        public override bool CanWrite => false;

        public override long Length => inner.Length;

        public override long Position
        {
            get => inner.Position;
            set => inner.Position = value;
        }

        public override void Flush() => inner.Flush();

        public override int Read(byte[] buffer, int offset, int count)
        {
            return inner.Read(buffer, offset, count);
        }

        public override long Seek(long offset, SeekOrigin origin)
        {
            return inner.Seek(offset, origin);
        }

        public override void SetLength(long value) => throw new NotSupportedException();

        public override void Write(byte[] buffer, int offset, int count)
        {
            throw new NotSupportedException();
        }
    }

    internal static void ValidateXml(Stream stream, ref int remainingNodes, CancellationToken cancellationToken,
        Action<string, string>? validateElement = null)
    {
        using XmlReader reader = XmlReader.Create(stream, SecureXmlSettings());
        while (reader.Read())
        {
            cancellationToken.ThrowIfCancellationRequested();
            int cost = 1 + reader.AttributeCount;
            if (reader.Depth > 64 || cost > remainingNodes)
                throw new DiffBudgetExceededException();
            remainingNodes -= cost;
            if (reader.NodeType == XmlNodeType.Element)
                validateElement?.Invoke(reader.NamespaceURI, reader.LocalName);
        }
    }

    internal sealed class ExpandedByteBudget(long limit = OpenXmlExtractionLimits.MaxExpandedXmlBytes)
    {
        private long _consumed;

        public void Consume(int count)
        {
            _consumed = checked(_consumed + count);
            if (_consumed > limit)
            {
                throw new DiffBudgetExceededException();
            }
        }
    }

    internal sealed class BudgetedEntryStream(
        Stream inner,
        ExpandedByteBudget budget,
        long partLimit = OpenXmlExtractionLimits.MaxXmlPartBytes,
        long? expectedLength = null) : Stream
    {
        private long _entryBytes;

        public override bool CanRead => inner.CanRead;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position
        {
            get => throw new NotSupportedException();
            set => throw new NotSupportedException();
        }

        public override int Read(byte[] buffer, int offset, int count)
        {
            int read = inner.Read(buffer, offset, count);
            Consume(read, count > 0);
            return read;
        }

        public override int Read(Span<byte> buffer)
        {
            int read = inner.Read(buffer);
            Consume(read, buffer.Length > 0);
            return read;
        }

        public override async ValueTask<int> ReadAsync(
            Memory<byte> buffer,
            CancellationToken cancellationToken = default)
        {
            int read = await inner.ReadAsync(buffer, cancellationToken)
                .ConfigureAwait(false);
            Consume(read, buffer.Length > 0);
            return read;
        }

        protected override void Dispose(bool disposing)
        {
            if (disposing)
            {
                inner.Dispose();
            }
            base.Dispose(disposing);
        }

        public override ValueTask DisposeAsync() => inner.DisposeAsync();
        public override void Flush() => throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin)
            => throw new NotSupportedException();
        public override void SetLength(long value)
            => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count)
            => throw new NotSupportedException();

        private void Consume(int count, bool requestedData)
        {
            _entryBytes = checked(_entryBytes + count);
            if (_entryBytes > partLimit)
            {
                throw new DiffBudgetExceededException();
            }
            budget.Consume(count);
            if (expectedLength is { } expected &&
                (_entryBytes > expected || requestedData && count == 0 && _entryBytes != expected))
                throw new InvalidDataException("Package part length differs from its declaration.");
        }
    }

    internal sealed class DiffBudgetExceededException : Exception
    {
    }
}
