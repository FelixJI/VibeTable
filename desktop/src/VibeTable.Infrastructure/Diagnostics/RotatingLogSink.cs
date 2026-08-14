using System.Text;

namespace VibeTable.Infrastructure.Diagnostics;

/// <summary>
/// Size/day bounded UTF-8 diagnostic sink. Rotation is local to one process
/// pump; callers pass already-redacted JSON lines.
/// </summary>
public sealed class RotatingLogSink : IAsyncDisposable
{
    public const long MaximumFileBytes = 5L * 1024 * 1024;
    public const long MaximumDirectoryBytes = 50L * 1024 * 1024;
    public const int RetentionDays = 7;

    private readonly string _path;
    private StreamWriter? _writer;
    private DateOnly _openedDay;
    private long _bytes;

    public RotatingLogSink(string path)
    {
        _path = Path.GetFullPath(path);
    }

    public async ValueTask WriteLineAsync(string line)
    {
        ArgumentNullException.ThrowIfNull(line);
        int incoming = Encoding.UTF8.GetByteCount(line)
            + Encoding.UTF8.GetByteCount(Environment.NewLine);
        DateOnly today = DateOnly.FromDateTime(DateTime.UtcNow);
        if (_writer is null)
        {
            await OpenAsync(today, incoming).ConfigureAwait(false);
        }
        else if (_openedDay != today || _bytes + incoming > MaximumFileBytes)
        {
            await RotateAsync(today).ConfigureAwait(false);
        }
        await _writer!.WriteLineAsync(line).ConfigureAwait(false);
        await _writer.FlushAsync().ConfigureAwait(false);
        _bytes += incoming;
    }

    private async ValueTask OpenAsync(DateOnly today, int incoming)
    {
        string directory = ResolveDirectory();
        Directory.CreateDirectory(directory);
        if (File.Exists(_path))
        {
            var current = new FileInfo(_path);
            DateOnly writtenDay = DateOnly.FromDateTime(current.LastWriteTimeUtc);
            if (writtenDay != today || current.Length + incoming > MaximumFileBytes)
            {
                await RotateAsync(today).ConfigureAwait(false);
                return;
            }
        }
        Prune(directory);
        OpenWriter(today);
    }

    private async ValueTask RotateAsync(DateOnly today)
    {
        if (_writer is not null)
        {
            await _writer.DisposeAsync().ConfigureAwait(false);
            _writer = null;
        }
        string directory = ResolveDirectory();
        Directory.CreateDirectory(directory);
        if (File.Exists(_path) && new FileInfo(_path).Length > 0)
        {
            string rotated = Path.Combine(
                directory,
                $"{Path.GetFileNameWithoutExtension(_path)}-{DateTime.UtcNow:yyyyMMdd-HHmmssfff}{Path.GetExtension(_path)}");
            File.Move(_path, rotated);
        }
        Prune(directory);
        OpenWriter(today);
    }

    private string ResolveDirectory()
    {
        string? directory = Path.GetDirectoryName(_path);
        if (string.IsNullOrWhiteSpace(directory))
        {
            throw new InvalidOperationException("Diagnostic log path has no directory.");
        }
        return directory;
    }

    private void OpenWriter(DateOnly today)
    {
        _writer = new StreamWriter(_path, append: true, new UTF8Encoding(false));
        _openedDay = today;
        _bytes = File.Exists(_path) ? new FileInfo(_path).Length : 0;
    }

    private static void Prune(string directory)
    {
        DateTime cutoff = DateTime.UtcNow.AddDays(-RetentionDays);
        FileInfo[] files = new DirectoryInfo(directory)
            .GetFiles("*.log")
            .OrderByDescending(file => file.LastWriteTimeUtc)
            .ToArray();
        long retained = 0;
        foreach (FileInfo file in files)
        {
            retained += file.Length;
            if (file.LastWriteTimeUtc < cutoff || retained > MaximumDirectoryBytes)
            {
                try { file.Delete(); } catch (IOException) { }
                catch (UnauthorizedAccessException) { }
            }
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (_writer is not null)
        {
            await _writer.DisposeAsync().ConfigureAwait(false);
            _writer = null;
        }
    }
}
