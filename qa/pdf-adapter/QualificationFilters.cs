using ICSharpCode.SharpZipLib.Zip.Compression;
using UglyToad.PdfPig.Filters;
using UglyToad.PdfPig.Tokens;

namespace PdfAdapterQualification;

internal sealed class PdfBudgetException(string code) : Exception(code)
{
    public string Code { get; } = code;
}

internal sealed class PdfStreamException : Exception { }

internal sealed class DecodeBudget
{
    public const long SingleStream = 32L * 1024 * 1024;
    private const long TotalDecoded = 256L * 1024 * 1024;
    private long _decoded;

    public void Add(long streamBytes, int count)
    {
        if (streamBytes + count > SingleStream)
            throw new PdfBudgetException("extract.pdf_stream_limit");
        if (_decoded + count > TotalDecoded)
            throw new PdfBudgetException("extract.pdf_total_limit");
        _decoded += count;
    }
}

internal sealed class QualificationFilterProvider : IFilterProvider
{
    private readonly DecodeBudget _budget = new();

    private IFilter Wrap(IFilter filter) => filter is FlateFilter
        ? new StrictFlateFilter(_budget)
        : new CountedFilter(filter, _budget);

    public IReadOnlyList<IFilter> GetFilters(DictionaryToken dictionary)
        => DefaultFilterProvider.Instance.GetFilters(dictionary).Select(Wrap).ToArray();

    public IReadOnlyList<IFilter> GetNamedFilters(IReadOnlyList<NameToken> names)
        => DefaultFilterProvider.Instance.GetNamedFilters(names).Select(Wrap).ToArray();

    public IReadOnlyList<IFilter> GetAllFilters()
        => DefaultFilterProvider.Instance.GetAllFilters().Select(Wrap).ToArray();
}

internal sealed class CountedFilter(IFilter inner, DecodeBudget budget) : IFilter
{
    public bool IsSupported => inner.IsSupported;

    public Memory<byte> Decode(
        Memory<byte> input, DictionaryToken dictionary, IFilterProvider provider, int filterIndex)
    {
        // Non-Flate decoding may allocate before returning; the process limit bounds that work.
        var decoded = inner.Decode(input, dictionary, provider, filterIndex);
        budget.Add(0, decoded.Length);
        return decoded;
    }
}

internal sealed class StrictFlateFilter(DecodeBudget budget) : IFilter
{
    public bool IsSupported => true;

    public Memory<byte> Decode(
        Memory<byte> input, DictionaryToken dictionary, IFilterProvider provider, int filterIndex)
    {
        // PdfPig includes a delimiter after a direct xref /Length. Trim only that delimiter.
        if (filterIndex == 0
            && dictionary.TryGet<NameToken>(NameToken.Type, out var type) && type.Data == "XRef"
            && dictionary.TryGet<NumericToken>(NameToken.Length, out var length)
            && length.Data >= 0 && length.Data < input.Length
            && length.Data == Math.Truncate(length.Data))
        {
            int declared = (int)length.Data;
            var suffix = input.Span[declared..];
            if (suffix.SequenceEqual("\n"u8) || suffix.SequenceEqual("\r\n"u8))
                input = input[..declared];
        }
        var parameters = DecodeParameterResolver.GetFilterParameters(dictionary, filterIndex);
        if (parameters.Data.Count != 0)
            throw new NotSupportedException("Predictor qualification is not complete.");
        if (input.Length < 2 || (input.Span[0] & 15) != 8 || (input.Span[0] >> 4) > 7)
            throw new PdfStreamException();
        var inflater = new Inflater(false);
        inflater.SetInput(input.ToArray());
        using var output = new MemoryStream();
        var buffer = new byte[8192];
        while (!inflater.IsFinished)
        {
            int count;
            try
            {
                count = inflater.Inflate(buffer);
            }
            catch (ICSharpCode.SharpZipLib.SharpZipBaseException)
            {
                throw new PdfStreamException();
            }
            if (count == 0)
            {
                if (inflater.IsFinished) break;
                if (inflater.IsNeedingDictionary)
                    throw new NotSupportedException("Preset dictionaries are unsupported.");
                throw new PdfStreamException();
            }
            budget.Add(output.Length, count);
            output.Write(buffer, 0, count);
        }
        if (inflater.RemainingInput != 0)
            throw new PdfStreamException();
        return output.ToArray();
    }
}

