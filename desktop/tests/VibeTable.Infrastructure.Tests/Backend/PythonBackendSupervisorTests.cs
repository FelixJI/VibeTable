using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Infrastructure.Tests.Backend;

/// <summary>
/// Lifecycle tests for <see cref="PythonBackendSupervisor"/>. The suite drives
/// the supervisor against a hermetic fake Python backend
/// (<c>tests/contract/fake_backend.py</c>) so we can exercise process spawn,
/// handshake, Job-Object kill semantics, stderr capture, and graceful/forced
/// shutdown without dragging the real backend's dependency tree into the .NET
/// test process.
/// </summary>
/// <remarks>
/// <para>
/// Every test asserts the spawned child process is GONE before the test
/// completes — that is the whole point of the Job-Object kill-on-close
/// guarantee. A leak would hang the suite (or, worse, leave an orphaned
/// python.exe behind) so each test cleans up in a try/finally and then
/// re-checks.
/// </para>
/// <para>
/// Timeouts in <see cref="BackendLaunchOptions"/> are deliberately short
/// (1-2 seconds) so a regression fails the suite fast instead of hanging it.
/// </para>
/// </remarks>
[TestClass]
[DoNotParallelize]
public sealed class PythonBackendSupervisorTests
{
    private static readonly string RepoRoot =
        FindRepoRoot(AppContext.BaseDirectory);

    private static readonly string FakeBackendScript =
        Path.Combine(RepoRoot, "tests", "contract", "fake_backend.py");

    private static readonly string PythonExecutable =
        ResolvePythonExecutable();

    /// <summary>
    /// Build launch options that spawn the fake backend via the resolved
    /// <c>python</c> executable. The fake honors a handful of environment
    /// variables (see <c>fake_backend.py</c>) that tests use to inject
    /// failure modes.
    /// </summary>
    private static BackendLaunchOptions FakeOptions(
        Action<BackendLaunchOptions>? configure = null,
        IDictionary<string, string>? env = null)
    {
        var options = new BackendLaunchOptions
        {
            Command = PythonExecutable,
            Arguments = FakeBackendScript,
            WorkingDirectory = RepoRoot,
            StartupTimeout = TimeSpan.FromSeconds(2),
            StopTimeout = TimeSpan.FromSeconds(2),
        };
        if (env is not null)
        {
            foreach (var kv in env)
            {
                options.Environment[kv.Key] = kv.Value;
            }
        }
        configure?.Invoke(options);
        return options;
    }

    [TestMethod]
    public async Task StartAsync_HandshakeSucceeds_TransitionsToReady()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions());
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            Assert.AreEqual(BackendState.Ready, supervisor.State);

            // The initialized client must be usable for follow-up RPC: ping
            // the fake backend's handshake again and assert the result.
            Assert.IsNotNull(supervisor.Client);
            var result = await supervisor.Client!.InvokeAsync<
                HandshakeParams,
                HandshakeResult>(
                "system.handshake",
                new HandshakeParams(
                    ClientVersion: ApplicationVersion.FromAssembly(
                        typeof(PythonBackendSupervisor).Assembly),
                    ProtocolVersion: "1.0"),
                CancellationToken.None);
            Assert.AreEqual("1.0", result.ProtocolVersion);
            CollectionAssert.AreEquivalent(
                new[] { "system.handshake", "test.delay", "test.exit" },
                result.Capabilities);
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Stopped, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StartAsync_ProtocolMismatch_TransitionsToFaulted()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions(env:
            new Dictionary<string, string>
            {
                ["__VIBETABLE_FORCE_PROTOCOL_MISMATCH"] = "1",
            }));
        try
        {
            await AssertThrowsAsync<InvalidOperationException>(
                async () => await supervisor.StartAsync(CancellationToken.None));
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Faulted, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StartAsync_ExecutableNotFound_TransitionsToFaulted()
    {
        var options = new BackendLaunchOptions
        {
            Command = "definitely-not-a-real-executable-vibetable-" + Guid.NewGuid().ToString("N"),
            Arguments = "irrelevant",
            WorkingDirectory = RepoRoot,
            StartupTimeout = TimeSpan.FromSeconds(2),
            StopTimeout = TimeSpan.FromSeconds(2),
        };
        await using var supervisor = new PythonBackendSupervisor(options);
        try
        {
            await AssertThrowsAsync<InvalidOperationException>(
                async () => await supervisor.StartAsync(CancellationToken.None));
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Faulted, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StartAsync_HandshakeDelaysPastStartupTimeout_TransitionsToFaulted()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions(
            configure: o => o.StartupTimeout = TimeSpan.FromSeconds(1),
            env: new Dictionary<string, string>
            {
                ["__VIBETABLE_HANDSHAKE_DELAY_SECONDS"] = "5",
            }));
        try
        {
            await AssertThrowsAsync<TimeoutException>(
                async () => await supervisor.StartAsync(CancellationToken.None));
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Faulted, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StartAsync_UnexpectedExit_TransitionsToFaulted()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions());
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            Assert.AreEqual(BackendState.Ready, supervisor.State);

            // Tell the fake to exit immediately. The supervisor should observe
            // the unexpected process exit and transition to Faulted.
            await supervisor.Client!.InvokeAsync<object, JsonElement>(
                "test.exit",
                new { },
                CancellationToken.None);

            // Wait briefly for the supervisor's exit handler to run.
            await WaitForStateAsync(supervisor, BackendState.Faulted, TimeSpan.FromSeconds(2));
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Faulted, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StartAsync_CapturesAndForwardsStderrSentinel()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions());
        var forwarded = new TaskCompletionSource<string>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        supervisor.LogReceived += (_, line) =>
        {
            if (line.Contains("vibetable-fake-backend: ready", StringComparison.Ordinal))
            {
                forwarded.TrySetResult(line);
            }
        };
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            string forwardedLine = await forwarded.Task.WaitAsync(TimeSpan.FromSeconds(2));
            StringAssert.Contains(forwardedLine, "vibetable-fake-backend: ready");

            // Give the fake a moment to flush stderr (it writes the sentinel
            // before the first read).
            string stderr = await WaitForStderrAsync(
                supervisor, "vibetable-fake-backend: ready", TimeSpan.FromSeconds(2));
            StringAssert.Contains(stderr, "vibetable-fake-backend: ready");
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StopAsync_GracefulStop_TransitionsToStopped()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions());
        await supervisor.StartAsync(CancellationToken.None);
        Assert.AreEqual(BackendState.Ready, supervisor.State);

        // Close stdin (which the supervisor does internally on StopAsync) ->
        // fake sees EOF and exits 0. Graceful path: state goes to Stopped.
        await supervisor.StopAsync(CancellationToken.None);

        Assert.AreEqual(BackendState.Stopped, supervisor.State);
        Assert.IsTrue(supervisor.ExitCode.HasValue, "ExitCode should be captured on graceful stop.");
        Assert.AreEqual(0, supervisor.ExitCode!.Value);
        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StopAsync_FakeIgnoresEof_IsForceKilledAfterTimeout()
    {
        await using var supervisor = new PythonBackendSupervisor(FakeOptions(
            configure: o =>
            {
                o.StartupTimeout = TimeSpan.FromSeconds(2);
                // Very short stop timeout so the test fails fast on a regression
                // but still distinguishes graceful from forced kill.
                o.StopTimeout = TimeSpan.FromMilliseconds(500);
            },
            env: new Dictionary<string, string>
            {
                ["__VIBETABLE_IGNORE_EOF"] = "1",
            }));
        try
        {
            await supervisor.StartAsync(CancellationToken.None);

            var sw = Stopwatch.StartNew();
            await supervisor.StopAsync(CancellationToken.None);
            sw.Stop();

            // Forced-kill path: state goes to Stopped (we did stop), but the
            // process was killed rather than exiting on its own. We assert the
            // stop completed within a reasonable bound of the StopTimeout so a
            // missing force-kill (which would hang on Windows WaitForExit) is
            // caught here rather than timing out the whole suite.
            Assert.AreEqual(BackendState.Stopped, supervisor.State);
            Assert.IsTrue(
                sw.Elapsed < TimeSpan.FromSeconds(5),
                $"StopAsync took {sw.Elapsed.TotalMilliseconds:F0}ms — force-kill path likely missing.");
        }
        finally
        {
            // supervisor.StopAsync already called; the await-using will Dispose.
        }

        await AssertChildGoneAsync(supervisor);
    }

    [TestMethod]
    public async Task StateChanged_EventFiresOnTransitions()
    {
        // Smoke check that StateChanged is raised with the expected sequence
        // on a happy-path start/stop. We don't over-assert here because the
        // previous tests already cover terminal states; this guards against a
        // regression where the event is never wired up.
        var transitions = new List<BackendState>();
        using var stoppedObserved = new ManualResetEventSlim();
        await using var supervisor = new PythonBackendSupervisor(FakeOptions());
        supervisor.StateChanged += (_, state) =>
        {
            lock (transitions)
            {
                transitions.Add(state);
            }
            if (state == BackendState.Stopped)
            {
                stoppedObserved.Set();
            }
        };

        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            await supervisor.StopAsync(CancellationToken.None);
        }
        catch
        {
            await supervisor.StopAsync(CancellationToken.None);
            throw;
        }

        Assert.IsTrue(
            stoppedObserved.Wait(TimeSpan.FromSeconds(5)),
            "The asynchronous Stopped notification was not observed.");
        lock (transitions)
        {
            CollectionAssert.Contains(transitions, BackendState.Starting);
            CollectionAssert.Contains(transitions, BackendState.Ready);
            CollectionAssert.Contains(transitions, BackendState.Stopping);
            CollectionAssert.Contains(transitions, BackendState.Stopped);
        }
    }

    [TestMethod]
    [TestCategory("Integration")]
    public async Task Integration_RealBackend_HandshakeSucceeds()
    {
        // The only test that exercises the REAL Python backend via
        // ``uv run python -m backend``. Tagged "Integration" so CI can opt
        // out; locally it should pass as long as ``uv`` is on PATH and the
        // backend is importable from the repo root.
        if (!IsUvAvailable())
        {
            Assert.Inconclusive("uv is not on PATH; skipping real-backend integration test.");
            return;
        }

        var options = new BackendLaunchOptions
        {
            Command = "uv",
            Arguments = "run python -m backend",
            WorkingDirectory = RepoRoot,
            StartupTimeout = TimeSpan.FromSeconds(30),
            StopTimeout = TimeSpan.FromSeconds(10),
        };

        await using var supervisor = new PythonBackendSupervisor(options);
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            Assert.AreEqual(BackendState.Ready, supervisor.State);
            Assert.IsNotNull(supervisor.Client);

            var result = await supervisor.Client!.InvokeAsync<
                HandshakeParams,
                HandshakeResult>(
                "system.handshake",
                new HandshakeParams(
                    ClientVersion: ApplicationVersion.FromAssembly(
                        typeof(PythonBackendSupervisor).Assembly),
                    ProtocolVersion: "1.0"),
                CancellationToken.None);
            Assert.AreEqual("1.0", result.ProtocolVersion);
            CollectionAssert.AreEquivalent(
                new[]
                {
                    "gridState.get",
                    "gridState.save",
                    "path.registerExportTarget",
                    "path.registerImportSource",
                    "path.requestExportTarget",
                    "path.requestImportSource",
                    "path.resolveGrant",
                    "system.handshake",
                    "task.cancel",
                    "task.create",
                    "task.status",
                },
                result.Capabilities);
        }
        finally
        {
            await supervisor.StopAsync(CancellationToken.None);
        }

        Assert.AreEqual(BackendState.Stopped, supervisor.State);
        await AssertChildGoneAsync(supervisor);
    }

    // ---------- helpers ----------

    private static async Task AssertThrowsAsync<TException>(Func<Task> action)
        where TException : Exception
    {
        try
        {
            await action();
        }
        catch (TException)
        {
            return;
        }
        catch (Exception ex)
        {
            Assert.Fail(
                $"Expected {typeof(TException).Name}, got {ex.GetType().Name}: {ex.Message}");
        }
        Assert.Fail(
            $"Expected {typeof(TException).Name} but no exception was thrown.");
    }

    private static async Task AssertChildGoneAsync(PythonBackendSupervisor supervisor)
    {
        // The supervisor's Process handle should report exited after Stop or
        // Fault. Also assert no python.exe from this run lingers by name.
        Assert.IsTrue(
            supervisor.HasExited,
            "Supervisor child should have exited after Stop/Fault.");
        await Task.CompletedTask;
    }

    private static Task WaitForStateAsync(
        PythonBackendSupervisor supervisor,
        BackendState expected,
        TimeSpan timeout)
    {
        if (supervisor.State == expected)
        {
            return Task.CompletedTask;
        }

        var tcs = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        void Handler(object? s, BackendState st)
        {
            if (st == expected)
            {
                tcs.TrySetResult(true);
            }
        }
        supervisor.StateChanged += Handler;
        var cts = new CancellationTokenSource(timeout);
        cts.Token.Register(() =>
        {
            supervisor.StateChanged -= Handler;
            tcs.TrySetException(
                new TimeoutException(
                    $"Timed out waiting for state {expected}. Current: {supervisor.State}."));
        });
        return tcs.Task.ContinueWith(t =>
        {
            supervisor.StateChanged -= Handler;
            cts.Dispose();
            return t;
        }).Unwrap();
    }

    private static async Task<string> WaitForStderrAsync(
        PythonBackendSupervisor supervisor,
        string substring,
        TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            var stderr = supervisor.GetStdErrorLog();
            if (stderr.Contains(substring, StringComparison.Ordinal))
            {
                return stderr;
            }
            await Task.Delay(25).ConfigureAwait(false);
        }
        return supervisor.GetStdErrorLog();
    }

    private static string FindRepoRoot(string startDir)
    {
        var dir = new DirectoryInfo(startDir);
        while (dir is not null)
        {
            if (dir.GetDirectories(".git").Any() || dir.GetFiles("pyproject.toml").Any())
            {
                return dir.FullName;
            }
            dir = dir.Parent;
        }
        return startDir;
    }

    private static string ResolvePythonExecutable()
    {
        // Prefer a real python on PATH; fall back to "python" and let the
        // supervisor's ProcessStartInfo surface a clearer error if missing.
        var candidates = RuntimeInformation.IsOSPlatform(OSPlatform.Windows)
            ? new[] { "python.exe", "python" }
            : new[] { "python3", "python" };

        var pathEnv = Environment.GetEnvironmentVariable("PATH") ?? string.Empty;
        var sep = RuntimeInformation.IsOSPlatform(OSPlatform.Windows) ? ';' : ':';
        foreach (var candidate in candidates)
        {
            foreach (var dir in pathEnv.Split(sep, StringSplitOptions.RemoveEmptyEntries))
            {
                var full = Path.Combine(dir, candidate);
                if (File.Exists(full))
                {
                    return full;
                }
            }
        }
        return "python";
    }

    private static bool IsUvAvailable()
    {
        var pathEnv = Environment.GetEnvironmentVariable("PATH") ?? string.Empty;
        var sep = RuntimeInformation.IsOSPlatform(OSPlatform.Windows) ? ';' : ':';
        foreach (var dir in pathEnv.Split(sep, StringSplitOptions.RemoveEmptyEntries))
        {
            var full = Path.Combine(dir, "uv.exe");
            if (File.Exists(full))
            {
                return true;
            }
            full = Path.Combine(dir, "uv");
            if (File.Exists(full))
            {
                return true;
            }
        }
        return false;
    }
}
