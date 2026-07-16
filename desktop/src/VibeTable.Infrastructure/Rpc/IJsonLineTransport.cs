using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Rpc;

/// <summary>
/// transports a single line-delimited JSON-RPC frame in each direction.
/// Reading returns one parsed <see cref="JsonElement"/> per call; writing
/// accepts a pre-serialized JSON line and appends the line terminator.
/// </summary>
public interface IJsonLineTransport : IAsyncDisposable
{
    /// <summary>
    /// Reads one JSON line and returns it as a parsed <see cref="JsonElement"/>.
    /// Returns <see langword="null"/> when the peer has cleanly closed the stream
    /// (clean EOF). Throws on malformed JSON or frames that exceed the agreed
    /// size limit.
    /// </summary>
    Task<JsonElement?> ReadAsync(CancellationToken cancellationToken);

    /// <summary>
    /// Writes <paramref name="line"/> followed by a single line terminator.
    /// The caller is responsible for serializing compact JSON.
    /// </summary>
    Task WriteAsync(string line, CancellationToken cancellationToken);
}
