using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Infrastructure.PocketBase;

public sealed record PocketBaseProcessStartRequest(
    string FileName,
    string? WorkingDirectory,
    IReadOnlyList<string> Arguments,
    IReadOnlyDictionary<string, string> Environment);

public interface IPocketBaseProcessFactory
{
    IPocketBaseProcess Start(PocketBaseProcessStartRequest request);
}

public interface IPocketBaseProcess : IAsyncDisposable
{
    int Id { get; }
    TextReader StandardOutput { get; }
    TextReader StandardError { get; }
    bool HasExited { get; }
    int? ExitCode { get; }
    event EventHandler? Exited;

    void KillProcessTree();
    Task WaitForExitAsync(CancellationToken cancellationToken);
}

internal sealed class SystemPocketBaseProcessFactory : IPocketBaseProcessFactory
{
    public IPocketBaseProcess Start(PocketBaseProcessStartRequest request)
    {
        var startInfo = new ProcessStartInfo
        {
            FileName = request.FileName,
            WorkingDirectory = string.IsNullOrWhiteSpace(request.WorkingDirectory)
                ? AppContext.BaseDirectory
                : request.WorkingDirectory,
            UseShellExecute = false,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            RedirectStandardInput = false,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        foreach (string argument in request.Arguments)
        {
            startInfo.ArgumentList.Add(argument);
        }
        foreach ((string name, string value) in request.Environment)
        {
            startInfo.Environment[name] = value;
        }

        var process = new Process
        {
            StartInfo = startInfo,
            EnableRaisingEvents = true,
        };
        JobObject job = JobObject.Create();
        try
        {
            process.Start();
            if (JobObject.IsSupported)
            {
                job.AssignProcess(process.SafeHandle.DangerousGetHandle());
            }
            return new SystemPocketBaseProcess(process, job);
        }
        catch (Win32Exception exception)
        {
            process.Dispose();
            job.Dispose();
            throw new InvalidOperationException(
                $"Unable to start the local data sidecar: {exception.Message}",
                exception);
        }
        catch
        {
            try
            {
                if (!process.HasExited)
                {
                    process.Kill(entireProcessTree: true);
                }
            }
            catch
            {
                // The job close below is the final cleanup boundary.
            }
            process.Dispose();
            job.Dispose();
            throw;
        }
    }
}

internal sealed class SystemPocketBaseProcess(
    Process process,
    JobObject job) : IPocketBaseProcess
{
    private int _disposed;

    public int Id => process.Id;
    public TextReader StandardOutput => process.StandardOutput;
    public TextReader StandardError => process.StandardError;
    public bool HasExited
    {
        get
        {
            try
            {
                return process.HasExited;
            }
            catch (InvalidOperationException)
            {
                return true;
            }
        }
    }
    public int? ExitCode
    {
        get
        {
            try
            {
                return process.HasExited ? process.ExitCode : null;
            }
            catch (InvalidOperationException)
            {
                return null;
            }
        }
    }

    public event EventHandler? Exited
    {
        add => process.Exited += value;
        remove => process.Exited -= value;
    }

    public void KillProcessTree()
    {
        if (!HasExited)
        {
            process.Kill(entireProcessTree: true);
        }
    }

    public Task WaitForExitAsync(CancellationToken cancellationToken)
        => process.WaitForExitAsync(cancellationToken);

    public ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return ValueTask.CompletedTask;
        }

        try
        {
            if (!HasExited)
            {
                process.Kill(entireProcessTree: true);
                process.WaitForExit(2000);
            }
        }
        catch
        {
            // Closing the kill-on-close job is the backstop.
        }
        process.Dispose();
        job.Dispose();
        return ValueTask.CompletedTask;
    }
}
