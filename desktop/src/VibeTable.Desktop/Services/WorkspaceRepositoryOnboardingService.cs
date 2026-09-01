using System.Diagnostics;
using System.IO;
using System.Runtime.ExceptionServices;
using System.Security.Cryptography;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public interface ITrustedSidecarProcessRunner
{
    Task<TrustedSidecarProcessResult> RunAsync(
        ProcessStartInfo startInfo,
        string? standardInput,
        CancellationToken cancellationToken);
}

/// <summary>
/// Runs the bundled Sidecar's one-shot repository init/unlock modes. Paths
/// and identity are environment-only; recovery material is stdout/stdin-only.
/// No secret is placed in argv, environment, diagnostics, or WebView payloads.
/// </summary>
public sealed class WorkspaceRepositoryOnboardingService
{
    private const int MaxTrustedOutputCharacters = 8 * 1024;
    private readonly Func<PocketBaseLaunchOptions> _optionsFactory;
    private readonly Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryAuthority>
        _authorityFactory;
    private readonly ITrustedSidecarProcessRunner _runner;

    public WorkspaceRepositoryOnboardingService(
        Func<PocketBaseLaunchOptions> optionsFactory,
        Func<WorkspaceRegistryEntryV2, WorkspaceRepositoryAuthority>?
            authorityFactory = null,
        ITrustedSidecarProcessRunner? runner = null)
    {
        _optionsFactory = optionsFactory
            ?? throw new ArgumentNullException(nameof(optionsFactory));
        _authorityFactory = authorityFactory
            ?? (_ => new WorkspaceRepositoryAuthority(1, Guid.NewGuid()));
        _runner = runner ?? new TrustedSidecarProcessRunner();
    }

    public async Task<WorkspaceRepositoryInitialization> InitializeAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        PocketBaseLaunchOptions options = _optionsFactory();
        ProcessStartInfo start = CreateStartInfo(
            options,
            workspace,
            "--initialize-workspace-repository",
            _authorityFactory(workspace));
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        timeout.CancelAfter(options.StartupTimeout);
        TrustedSidecarProcessResult result = await _runner.RunAsync(
            start,
            standardInput: null,
            timeout.Token).ConfigureAwait(false);
        EnsureSucceeded(result);
        using JsonDocument document = ParseBounded(result.StandardOutput);
        JsonElement root = document.RootElement;
        string[] names = root.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        string workspaceId = RequiredString(root, "workspaceId");
        string encryptionMode = RequiredString(root, "encryptionMode");
        bool initialized = root.TryGetProperty(
            "initialized",
            out JsonElement initializedElement)
            && initializedElement.ValueKind == JsonValueKind.True;
        string? recoveryKey = root.TryGetProperty(
            "recoveryKey",
            out JsonElement recovery)
            && recovery.ValueKind == JsonValueKind.String
                ? recovery.GetString()
                : null;
        string[] expected = recoveryKey is null
            ? ["encryptionMode", "initialized", "workspaceId"]
            : ["encryptionMode", "initialized", "recoveryKey", "workspaceId"];
        if (!names.SequenceEqual(expected, StringComparer.Ordinal) ||
            !initialized ||
            workspaceId != workspace.WorkspaceId.ToString("D").ToLowerInvariant())
            throw InvalidOutput();

        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace));
        string expectedMode = manifest.EncryptionMode switch
        {
            WorkspaceEncryptionMode.None => "none",
            WorkspaceEncryptionMode.Convenient => "convenient",
            WorkspaceEncryptionMode.Protected => "protected",
            _ => throw new ArgumentOutOfRangeException(),
        };
        if (!string.Equals(
                encryptionMode,
                expectedMode,
                StringComparison.Ordinal) ||
            (manifest.EncryptionMode == WorkspaceEncryptionMode.Protected) !=
                (recoveryKey is not null))
            throw InvalidOutput();
        if (recoveryKey is not null)
            ValidateRecoveryKey(recoveryKey);
        return new WorkspaceRepositoryInitialization(
            workspace.WorkspaceId,
            manifest.EncryptionMode,
            recoveryKey);
    }

    public async Task UnlockAsync(
        WorkspaceRegistryEntryV2 workspace,
        string recoveryKey,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        ValidateRecoveryKey(recoveryKey);
        PocketBaseLaunchOptions options = _optionsFactory();
        ProcessStartInfo start = CreateStartInfo(
            options,
            workspace,
            "--unlock-workspace-repository",
            new WorkspaceRepositoryAuthority(1, Guid.NewGuid()));
        // Recovery material exists only in this trusted stdin payload.
        string input = JsonSerializer.Serialize(new { recoveryKey });
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        timeout.CancelAfter(options.StartupTimeout);
        TrustedSidecarProcessResult result = await _runner.RunAsync(
            start,
            input,
            timeout.Token).ConfigureAwait(false);
        EnsureSucceeded(result);
        using JsonDocument document = ParseBounded(result.StandardOutput);
        JsonElement root = document.RootElement;
        string[] names = root.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (!names.SequenceEqual(
                ["unlocked", "workspaceId"],
                StringComparer.Ordinal) ||
            !root.TryGetProperty(
                "unlocked",
                out JsonElement unlocked) ||
            unlocked.ValueKind != JsonValueKind.True ||
            RequiredString(root, "workspaceId") !=
                workspace.WorkspaceId.ToString("D").ToLowerInvariant())
            throw InvalidOutput();
    }

    public bool HasPendingKeyRotation(
        WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        WorkspacePaths paths = WorkspaceLayout.Paths(
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace));
        return File.Exists(Path.Combine(
            paths.Coordination,
            "key-rotation-intent.json"));
    }

    public async Task<string> RotatePendingKeyAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        PocketBaseLaunchOptions options = _optionsFactory();
        ProcessStartInfo start = CreateStartInfo(
            options,
            workspace,
            "--rotate-workspace-repository",
            _authorityFactory(workspace));
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        timeout.CancelAfter(options.StartupTimeout);
        TrustedSidecarProcessResult result = await _runner.RunAsync(
            start,
            standardInput: null,
            timeout.Token).ConfigureAwait(false);
        EnsureSucceeded(result);
        using JsonDocument document = ParseBounded(result.StandardOutput);
        JsonElement root = document.RootElement;
        string[] names = root.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        string recoveryKey = RequiredString(root, "recoveryKey");
        if (!names.SequenceEqual(
                ["recoveryKey", "rotated", "workspaceId"],
                StringComparer.Ordinal)
            || !root.TryGetProperty("rotated", out JsonElement rotated)
            || rotated.ValueKind != JsonValueKind.True
            || RequiredString(root, "workspaceId") !=
                workspace.WorkspaceId.ToString("D").ToLowerInvariant())
        {
            throw InvalidOutput();
        }
        ValidateRecoveryKey(recoveryKey);
        return recoveryKey;
    }

    internal static ProcessStartInfo CreateStartInfo(
        PocketBaseLaunchOptions options,
        WorkspaceRegistryEntryV2 workspace,
        string oneShotFlag,
        WorkspaceRepositoryAuthority authority)
    {
        ArgumentNullException.ThrowIfNull(options);
        ArgumentNullException.ThrowIfNull(workspace);
        if (oneShotFlag is not (
                "--initialize-workspace-repository" or
                "--unlock-workspace-repository" or
                "--rotate-workspace-repository" or
                "--initialize-workspace-replica" or
                "--recover-workspace-replica" or
                "--verify-workspace-replica"))
            throw new ArgumentOutOfRangeException(nameof(oneShotFlag));
        var start = new ProcessStartInfo
        {
            FileName = Path.GetFullPath(options.ExecutablePath),
            WorkingDirectory = options.WorkingDirectory
                ?? Path.GetDirectoryName(options.ExecutablePath)
                ?? AppContext.BaseDirectory,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardInput = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        start.ArgumentList.Add(oneShotFlag);
        foreach ((string key, string value) in options.Environment)
            start.Environment[key] = value;
        WorkspacePaths paths = WorkspaceLayout.Paths(
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace));
        start.Environment["VIBETABLE_SIDECAR_DATA_DIR"] = paths.Data;
        start.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"] =
            Convert.ToHexString(RandomNumberGenerator.GetBytes(32));
        start.Environment["VIBETABLE_WORKSPACE_ID"] =
            workspace.WorkspaceId.ToString("D").ToLowerInvariant();
        start.Environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"] = "1";
        if (authority.FenceEpoch == 0 || authority.ClaimId == Guid.Empty)
            throw new ArgumentException(
                "Repository authority is invalid.",
                nameof(authority));
        start.Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"] =
            authority.FenceEpoch.ToString(
                System.Globalization.CultureInfo.InvariantCulture);
        start.Environment["VIBETABLE_WORKSPACE_CLAIM_ID"] =
            authority.ClaimId.ToString("D").ToLowerInvariant();
        return start;
    }

    private static JsonDocument ParseBounded(string output)
    {
        if (string.IsNullOrWhiteSpace(output) ||
            output.Length > MaxTrustedOutputCharacters)
            throw InvalidOutput();
        try
        {
            return JsonDocument.Parse(output);
        }
        catch (JsonException)
        {
            throw InvalidOutput();
        }
    }

    private static void EnsureSucceeded(TrustedSidecarProcessResult result)
    {
        if (result.ExitCode != 0)
            throw new WorkspaceRegistryException(
                "repository.onboarding_failed",
                "The bundled Sidecar could not prepare the workspace repository.");
    }

    private static void ValidateRecoveryKey(string recoveryKey)
    {
        try
        {
            byte[] decoded = Convert.FromBase64String(
                recoveryKey.Replace('-', '+').Replace('_', '/') +
                new string('=', (4 - recoveryKey.Length % 4) % 4));
            if (decoded.Length != 32)
                throw InvalidOutput();
            CryptographicOperations.ZeroMemory(decoded);
        }
        catch (FormatException)
        {
            throw new WorkspaceRegistryException(
                "repository.recovery_key_invalid",
                "The recovery key is invalid.");
        }
    }

    private static string RequiredString(JsonElement root, string name)
        => root.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString())
                ? value.GetString()!
                : throw InvalidOutput();

    private static WorkspaceRegistryException InvalidOutput()
        => new(
            "repository.onboarding_response_invalid",
            "The bundled Sidecar returned an invalid onboarding response.");
}

internal interface ITrustedSidecarProcess : IDisposable
{
    bool HasExited { get; }

    int ExitCode { get; }

    bool Start();

    Task WriteStandardInputAsync(string input);

    void CloseStandardInput();

    Task<string> ReadStandardOutputToEndAsync(CancellationToken cancellationToken);

    Task<string> ReadStandardErrorToEndAsync(CancellationToken cancellationToken);

    Task WaitForExitAsync(CancellationToken cancellationToken);

    void KillEntireProcessTree();
}

internal sealed class SystemTrustedSidecarProcess(ProcessStartInfo startInfo) :
    ITrustedSidecarProcess
{
    private readonly Process _process = new() { StartInfo = startInfo };

    public bool HasExited => _process.HasExited;

    public int ExitCode => _process.ExitCode;

    public bool Start() => _process.Start();

    public Task WriteStandardInputAsync(string input) =>
        _process.StandardInput.WriteAsync(input);

    public void CloseStandardInput() => _process.StandardInput.Close();

    public Task<string> ReadStandardOutputToEndAsync(
        CancellationToken cancellationToken) =>
        _process.StandardOutput.ReadToEndAsync(cancellationToken);

    public Task<string> ReadStandardErrorToEndAsync(
        CancellationToken cancellationToken) =>
        _process.StandardError.ReadToEndAsync(cancellationToken);

    public Task WaitForExitAsync(CancellationToken cancellationToken) =>
        _process.WaitForExitAsync(cancellationToken);

    public void KillEntireProcessTree() =>
        _process.Kill(entireProcessTree: true);

    public void Dispose() => _process.Dispose();
}

public sealed class TrustedSidecarProcessRunner :
    ITrustedSidecarProcessRunner
{
    private static readonly Task NeverCompletes =
        new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously).Task;
    private readonly Func<ProcessStartInfo, ITrustedSidecarProcess> _processFactory;

    public TrustedSidecarProcessRunner() :
        this(startInfo => new SystemTrustedSidecarProcess(startInfo))
    {
    }

    internal TrustedSidecarProcessRunner(
        Func<ProcessStartInfo, ITrustedSidecarProcess> processFactory)
    {
        _processFactory = processFactory
            ?? throw new ArgumentNullException(nameof(processFactory));
    }

    public async Task<TrustedSidecarProcessResult> RunAsync(
        ProcessStartInfo startInfo,
        string? standardInput,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(startInfo);
        ITrustedSidecarProcess process = _processFactory(startInfo);
        try
        {
            return await RunProcessAsync(
                process,
                standardInput,
                cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            TryDispose(process);
        }
    }

    private async Task<TrustedSidecarProcessResult> RunProcessAsync(
        ITrustedSidecarProcess process,
        string? standardInput,
        CancellationToken cancellationToken)
    {
        if (!process.Start())
            throw new WorkspaceRegistryException(
                "repository.onboarding_start_failed",
                "The bundled Sidecar onboarding helper could not start.");
        Task exitTask = ObserveExitAsync(process);
        Task? stdin = null;
        Task<string>? stdout = null;
        Task<string>? stderr = null;
        try
        {
            if (standardInput is not null)
            {
                stdin = process.WriteStandardInputAsync(standardInput);
                await stdin.WaitAsync(cancellationToken)
                    .ConfigureAwait(false);
            }
            process.CloseStandardInput();
            stdout = process.ReadStandardOutputToEndAsync(
                CancellationToken.None);
            stderr = process.ReadStandardErrorToEndAsync(
                CancellationToken.None);
            var outputFailure = new TaskCompletionSource<Exception>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            Task outputObservation = Task.WhenAll(
                ObserveFailureAsync(stdout, outputFailure),
                ObserveFailureAsync(stderr, outputFailure));
            Task first = await Task.WhenAny(exitTask, outputFailure.Task)
                .WaitAsync(cancellationToken)
                .ConfigureAwait(false);
            if (ReferenceEquals(first, outputFailure.Task))
            {
                Exception outputError = await outputFailure.Task
                    .ConfigureAwait(false);
                ExceptionDispatchInfo.Capture(outputError).Throw();
                throw new InvalidOperationException("Unreachable output path.");
            }
            await exitTask.WaitAsync(cancellationToken)
                .ConfigureAwait(false);
            await outputObservation.ConfigureAwait(false);
            string output = await stdout.ConfigureAwait(false);
            _ = await stderr.ConfigureAwait(false);
            return new TrustedSidecarProcessResult(
                process.ExitCode,
                output);
        }
        catch (Exception original)
        {
            ObserveInBackground(stdin);
            ObserveInBackground(stdout);
            ObserveInBackground(stderr);
            await TerminateAsync(process, exitTask).ConfigureAwait(false);
            await DrainAsync(stdin, stdout, stderr).ConfigureAwait(false);
            ExceptionDispatchInfo.Capture(original).Throw();
            throw new InvalidOperationException("Unreachable exception path.");
        }
    }

    internal static async Task TerminateAsync(
        ITrustedSidecarProcess process,
        Task exitTask)
    {
        bool exited = false;
        try
        {
            exited = process.HasExited;
        }
        catch
        {
            // An unreadable state is not proof that ownership ended.
        }
        if (!exited)
        {
            try
            {
                process.KillEntireProcessTree();
            }
            catch
            {
                // The sole exit observation remains the ownership boundary.
            }
        }
        try
        {
            await exitTask.ConfigureAwait(false);
            return;
        }
        catch
        {
            try
            {
                if (process.HasExited)
                    return;
            }
            catch
            {
                // Without an exit signal, cleanup ownership cannot transfer.
            }
        }
        await NeverCompletes.ConfigureAwait(false);
    }

    private static async Task ObserveExitAsync(ITrustedSidecarProcess process) =>
        await process.WaitForExitAsync(CancellationToken.None)
            .ConfigureAwait(false);

    private static async Task DrainAsync(
        Task? stdin,
        Task<string>? stdout,
        Task<string>? stderr)
    {
        await ObserveAsync(stdin).ConfigureAwait(false);
        await ObserveAsync(stdout).ConfigureAwait(false);
        await ObserveAsync(stderr).ConfigureAwait(false);
    }

    private static async Task ObserveAsync(Task? operation)
    {
        if (operation is null)
            return;
        try
        {
            await operation.ConfigureAwait(false);
        }
        catch
        {
            // Observation is read-only; preserve the initiating failure.
        }
    }

    private static void ObserveInBackground(Task? operation)
    {
        if (operation is not null)
            _ = ObserveAsync(operation);
    }

    private static async Task ObserveFailureAsync(
        Task operation,
        TaskCompletionSource<Exception> failure)
    {
        try
        {
            await operation.ConfigureAwait(false);
        }
        catch (Exception error)
        {
            failure.TrySetResult(error);
        }
    }

    private static void TryDispose(ITrustedSidecarProcess process)
    {
        try
        {
            process.Dispose();
        }
        catch
        {
            // Disposal must not replace the process or ownership failure.
        }
    }
}

public sealed record TrustedSidecarProcessResult(
    int ExitCode,
    string StandardOutput);

public sealed record WorkspaceRepositoryInitialization(
    Guid WorkspaceId,
    WorkspaceEncryptionMode EncryptionMode,
    string? RecoveryKey);

public sealed record WorkspaceRepositoryAuthority(
    ulong FenceEpoch,
    Guid ClaimId);

public sealed class WorkspaceKeyRotationPreOpenHook(
    WorkspaceRepositoryOnboardingService onboarding,
    IWorkspaceRepositoryRecoveryUi recoveryUi) : IWorkspacePreOpenHook
{
    private readonly WorkspaceRepositoryOnboardingService _onboarding =
        onboarding ?? throw new ArgumentNullException(nameof(onboarding));
    private readonly IWorkspaceRepositoryRecoveryUi _recoveryUi =
        recoveryUi ?? throw new ArgumentNullException(nameof(recoveryUi));

    public async Task PrepareAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken)
    {
        if (!_onboarding.HasPendingKeyRotation(workspace))
            return;
        string recoveryKey = await _onboarding.RotatePendingKeyAsync(
            workspace,
            cancellationToken);
        // This is the only presentation boundary for the new recovery key.
        // It never enters a Web message, log, argv, or environment variable.
        _recoveryUi.ConfirmRecoveryKey(
            workspace.DisplayName,
            recoveryKey);
    }
}
