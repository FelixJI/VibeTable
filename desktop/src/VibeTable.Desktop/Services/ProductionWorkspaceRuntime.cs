using System.IO;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Shell-only lifecycle plus factory for exactly one session-owned product
/// runtime. Calling <see cref="StartAsync"/> never starts data services; a
/// runtime exists only after <see cref="WorkspaceSessionManager.OpenAsync"/>.
/// </summary>
public sealed class ProductionWorkspaceRuntimeFactory :
    IWorkspaceRuntimeFactory,
    IBackendLifecycle,
    IProductSidecarGenerationAuthority,
    IAsyncDisposable
{
    private readonly object _gate = new();
    private readonly Func<PocketBaseLaunchOptions> _sidecarTemplateFactory;
    private readonly Func<BackendLaunchOptions> _backendTemplateFactory;
    private readonly DesktopWorkspaceAuthorityStore _authority;
    private readonly ProductSidecarGenerationSnapshotCache
        _productSidecarGenerations = new();
    private ProductionWorkspaceRuntime? _current;
    private bool _disposed;

    public ProductionWorkspaceRuntimeFactory(
        PocketBaseLaunchOptions sidecarTemplate,
        BackendLaunchOptions backendTemplate,
        IEnumerable<WorkspaceRegistryEntryV2>? knownWorkspaces = null)
        : this(
            () => sidecarTemplate,
            () => backendTemplate,
            knownWorkspaces)
    {
    }

    public ProductionWorkspaceRuntimeFactory(
        Func<PocketBaseLaunchOptions> sidecarTemplateFactory,
        Func<BackendLaunchOptions> backendTemplateFactory,
        IEnumerable<WorkspaceRegistryEntryV2>? knownWorkspaces = null)
    {
        _sidecarTemplateFactory = sidecarTemplateFactory
            ?? throw new ArgumentNullException(nameof(sidecarTemplateFactory));
        _backendTemplateFactory = backendTemplateFactory
            ?? throw new ArgumentNullException(nameof(backendTemplateFactory));
        _authority = new DesktopWorkspaceAuthorityStore();
        InitialSessionEpoch = knownWorkspaces is null
            ? 0
            : knownWorkspaces
                .Select(entry => _authority.TryRead(entry)?.LastSessionEpoch ?? 0)
                .Append(0UL)
                .Max();
    }

    public ulong InitialSessionEpoch { get; }

    public PythonBackendSupervisor? CurrentBackend
    {
        get
        {
            lock (_gate)
                return _current?.Backend;
        }
    }

    public PocketBaseSupervisor? CurrentSidecar
    {
        get
        {
            lock (_gate)
                return _current?.Sidecar;
        }
    }

    public WorkspaceV2HttpGateway? CurrentV2Gateway
    {
        get
        {
            lock (_gate)
                return _current?.Gateway;
        }
    }

    public WorkspaceV2SidecarCapabilities? CurrentCapabilities
    {
        get
        {
            lock (_gate)
                return _current?.Capabilities;
        }
    }

    public WorkspaceRegistryEntryV2? CurrentWorkspace
    {
        get
        {
            lock (_gate)
                return _current?.Workspace;
        }
    }

    public string? CurrentDataDirectory
    {
        get
        {
            lock (_gate)
                return _current?.DataDirectory;
        }
    }

    public string? CurrentPocketBaseVersion { get; private set; }

    public event Action? ClientReady;
    public event Action<Exception>? RecoveryFailed;
    public event Action? BindingChanged;
    private event Action? ProductSidecarCurrentChanged;

    event Action? IProductSidecarGenerationAuthority.CurrentChanged
    {
        add => ProductSidecarCurrentChanged += value;
        remove => ProductSidecarCurrentChanged -= value;
    }

    internal ProductSidecarGenerationSnapshot? CaptureProductSidecarGeneration()
    {
        lock (_gate)
        {
            if (_disposed || _current is null)
                return null;
            PocketBaseAdminContext? context = _current.Sidecar.GetAdminContext();
            WorkspaceV2SidecarCapabilities? capabilities = _current.Capabilities;
            if (context is null || capabilities is null)
                return null;
            return CreateProductSidecarGeneration(
                _current,
                context,
                capabilities);
        }
    }

    bool IProductSidecarGenerationAuthority.TryUseCurrent(
        ProductSidecarGenerationSnapshot snapshot,
        Func<bool> action)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        ArgumentNullException.ThrowIfNull(action);
        lock (_gate)
        {
            if (_disposed
                || _current is null
                || !ReferenceEquals(snapshot.RuntimeAuthority, _current))
                return false;
            PocketBaseAdminContext? context = _current.Sidecar.GetAdminContext();
            WorkspaceV2SidecarCapabilities? capabilities = _current.Capabilities;
            if (context is null || capabilities is null)
                return false;
            ProductSidecarGenerationSnapshot current =
                CreateProductSidecarGeneration(_current, context, capabilities);
            return ReferenceEquals(snapshot, current) && action();
        }
    }

    internal WorkspaceRepositoryAuthority PrepareRepositoryOnboarding(
        WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        DesktopWorkspaceAuthority authority = _authority.Prepare(workspace);
        return new WorkspaceRepositoryAuthority(
            authority.FenceEpoch,
            authority.ClaimId);
    }

    public IWorkspaceRuntime Create(
        WorkspaceRegistryEntryV2 workspace,
        ulong sessionEpoch)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        lock (_gate)
            ObjectDisposedException.ThrowIf(_disposed, this);
        DesktopWorkspaceAuthority authority =
            _authority.Reserve(workspace, sessionEpoch);
        PocketBaseLaunchOptions sidecarTemplate =
            _sidecarTemplateFactory()
            ?? throw new InvalidOperationException(
                "Sidecar launch options were not resolved.");
        BackendLaunchOptions backendTemplate =
            _backendTemplateFactory()
            ?? throw new InvalidOperationException(
                "Backend launch options were not resolved.");
        CurrentPocketBaseVersion =
            sidecarTemplate.ExpectedIdentity?.PocketBaseVersion;
        return new ProductionWorkspaceRuntime(
            this,
            workspace,
            authority,
            BuildSidecarOptions(workspace, authority, sidecarTemplate),
            BuildBackendOptions(workspace, authority, backendTemplate));
    }

    /// <summary>
    /// Deliberate no-op: loading the global shell never launches PocketBase
    /// or Python. WorkspaceSessionManager exclusively owns those processes.
    /// </summary>
    public Task StartAsync(CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        lock (_gate)
            ObjectDisposedException.ThrowIf(_disposed, this);
        return Task.CompletedTask;
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        ProductionWorkspaceRuntime? current;
        lock (_gate)
            current = _current;
        if (current is not null)
            await current.StopAsync(cancellationToken).ConfigureAwait(false);
    }

    public async ValueTask DisposeAsync()
    {
        ProductionWorkspaceRuntime? current;
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            current = _current;
            _current = null;
            _productSidecarGenerations.Clear();
        }
        if (current is not null)
            ProductSidecarCurrentChanged?.Invoke();
        if (current is not null)
            await current.DisposeAsync().ConfigureAwait(false);
    }

    internal void Activate(ProductionWorkspaceRuntime runtime)
    {
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (_current is not null && !ReferenceEquals(_current, runtime))
                throw new InvalidOperationException(
                    "Another workspace runtime is already active.");
            _current = runtime;
        }
        ProductSidecarCurrentChanged?.Invoke();
        BindingChanged?.Invoke();
        ClientReady?.Invoke();
    }

    internal void Deactivate(ProductionWorkspaceRuntime runtime)
    {
        bool changed = false;
        lock (_gate)
        {
            if (ReferenceEquals(_current, runtime))
            {
                _current = null;
                _productSidecarGenerations.Clear();
                changed = true;
            }
        }
        if (changed)
        {
            ProductSidecarCurrentChanged?.Invoke();
            BindingChanged?.Invoke();
        }
    }

    internal void NotifyClientReady(ProductionWorkspaceRuntime runtime)
    {
        lock (_gate)
        {
            if (!ReferenceEquals(_current, runtime))
                return;
        }
        ProductSidecarCurrentChanged?.Invoke();
        ClientReady?.Invoke();
    }

    internal void NotifyRecoveryFailed(
        ProductionWorkspaceRuntime runtime,
        Exception exception)
    {
        lock (_gate)
        {
            if (!ReferenceEquals(_current, runtime))
                return;
        }
        RecoveryFailed?.Invoke(exception);
    }

    private PocketBaseLaunchOptions BuildSidecarOptions(
        WorkspaceRegistryEntryV2 workspace,
        DesktopWorkspaceAuthority authority,
        PocketBaseLaunchOptions sidecarTemplate)
    {
        WorkspacePaths paths = WorkspaceLayout.Paths(
            RuntimeRoot(workspace));
        var environment = new Dictionary<string, string>(
            sidecarTemplate.Environment,
            StringComparer.Ordinal)
        {
            ["VIBETABLE_WORKSPACE_ID"] =
                workspace.WorkspaceId.ToString("D").ToLowerInvariant(),
            ["VIBETABLE_WORKSPACE_SESSION_EPOCH"] =
                authority.LastSessionEpoch.ToString(
                    System.Globalization.CultureInfo.InvariantCulture),
            ["VIBETABLE_WORKSPACE_FENCE_EPOCH"] =
                authority.FenceEpoch.ToString(
                    System.Globalization.CultureInfo.InvariantCulture),
            ["VIBETABLE_WORKSPACE_CLAIM_ID"] =
                authority.ClaimId.ToString("D").ToLowerInvariant(),
        };
        if (!string.IsNullOrWhiteSpace(workspace.ActivityRoot))
        {
            // The activity root remains the only writable runtime root. The
            // selected mirrored root is a separate, trusted host binding used
            // by the Sidecar's independently reopened replica adapter.
            environment["VIBETABLE_REPLICA_ROOT"] =
                Path.GetFullPath(workspace.SelectedRoot);
        }
        return new PocketBaseLaunchOptions
        {
            ExecutablePath = sidecarTemplate.ExecutablePath,
            WorkingDirectory = sidecarTemplate.WorkingDirectory,
            DataDirectory = paths.Data,
            LogPath = Path.Combine(paths.Temp, "logs", "pocketbase.log"),
            DevelopmentMode = sidecarTemplate.DevelopmentMode,
            StartupTimeout = sidecarTemplate.StartupTimeout,
            StopTimeout = sidecarTemplate.StopTimeout,
            HealthPollInterval = sidecarTemplate.HealthPollInterval,
            CrashRestartLimit = sidecarTemplate.CrashRestartLimit,
            CrashRestartInitialDelay =
                sidecarTemplate.CrashRestartInitialDelay,
            CrashRestartMaximumDelay =
                sidecarTemplate.CrashRestartMaximumDelay,
            ExpectedIdentity = sidecarTemplate.ExpectedIdentity,
            Environment = environment,
        };
    }

    private BackendLaunchOptions BuildBackendOptions(
        WorkspaceRegistryEntryV2 workspace,
        DesktopWorkspaceAuthority authority,
        BackendLaunchOptions backendTemplate)
    {
        WorkspacePaths paths = WorkspaceLayout.Paths(RuntimeRoot(workspace));
        var result = new BackendLaunchOptions
        {
            Command = backendTemplate.Command,
            Arguments = backendTemplate.Arguments,
            WorkingDirectory = backendTemplate.WorkingDirectory,
            StartupTimeout = backendTemplate.StartupTimeout,
            StopTimeout = backendTemplate.StopTimeout,
            LogPath = Path.Combine(paths.Temp, "logs", "backend.log"),
        };
        foreach ((string key, string value) in backendTemplate.Environment)
            result.Environment[key] = value;
        WorkspaceProcessEnvironment.Configure(
            result.Environment,
            paths.Data);
        result.Environment["VIBETABLE_WORKSPACE_ID"] =
            workspace.WorkspaceId.ToString("D").ToLowerInvariant();
        result.Environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"] =
            authority.LastSessionEpoch.ToString(
                System.Globalization.CultureInfo.InvariantCulture);
        result.Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"] =
            authority.FenceEpoch.ToString(
                System.Globalization.CultureInfo.InvariantCulture);
        result.Environment["VIBETABLE_WORKSPACE_CLAIM_ID"] =
            authority.ClaimId.ToString("D").ToLowerInvariant();
        return result;
    }

    internal static string RuntimeRoot(WorkspaceRegistryEntryV2 workspace)
        => Path.GetFullPath(
            string.IsNullOrWhiteSpace(workspace.ActivityRoot)
                ? workspace.SelectedRoot
                : workspace.ActivityRoot);

    private ProductSidecarGenerationSnapshot CreateProductSidecarGeneration(
        ProductionWorkspaceRuntime runtime,
        PocketBaseAdminContext context,
        WorkspaceV2SidecarCapabilities capabilities)
        => _productSidecarGenerations.GetOrCreate(
            runtime,
            context,
            new ProductSidecarIdentity(
                capabilities.WorkspaceId,
                capabilities.SessionEpoch,
                capabilities.FenceEpoch,
                capabilities.ClaimId),
            ProductRpcCapabilityManifest.Default
                .GetProductSidecarRegistrations());
}

public sealed class ProductionWorkspaceRuntime : IWorkspaceRuntime
{
    private readonly ProductionWorkspaceRuntimeFactory _owner;
    private readonly DesktopWorkspaceAuthority _authority;
    private readonly ProductRuntimeService _runtime;
    private readonly string _dataDirectory;
    private int _started;
    private int _disposed;

    internal ProductionWorkspaceRuntime(
        ProductionWorkspaceRuntimeFactory owner,
        WorkspaceRegistryEntryV2 workspace,
        DesktopWorkspaceAuthority authority,
        PocketBaseLaunchOptions sidecarOptions,
        BackendLaunchOptions backendOptions)
    {
        _owner = owner;
        Workspace = workspace;
        _authority = authority;
        _dataDirectory = Path.GetFullPath(sidecarOptions.DataDirectory);
        SidecarEnvironment = new Dictionary<string, string>(
            sidecarOptions.Environment,
            StringComparer.Ordinal);
        BackendEnvironment = new Dictionary<string, string>(
            backendOptions.Environment,
            StringComparer.Ordinal);
        ActivationPolicy = WorkspaceActivationPolicy.FromStageTimeouts(
            sidecarOptions.StartupTimeout,
            backendOptions.StartupTimeout);
        Sidecar = new PocketBaseSupervisor(sidecarOptions);
        var localData = new LocalDataService(Sidecar);
        Backend = new PythonBackendSupervisor(backendOptions);
        _runtime = new ProductRuntimeService(
            localData,
            Sidecar,
            Backend,
            backendOptions.Environment);
        Gateway = new WorkspaceV2HttpGateway(Sidecar);
        _runtime.ClientReady += OnClientReady;
        _runtime.RecoveryFailed += OnRecoveryFailed;
    }

    public WorkspaceRegistryEntryV2 Workspace { get; }
    public Guid WorkspaceId => Workspace.WorkspaceId;
    public ulong SessionEpoch => _authority.LastSessionEpoch;
    public WorkspaceActivationPolicy ActivationPolicy { get; }
    public PocketBaseSupervisor Sidecar { get; }
    public PythonBackendSupervisor Backend { get; }
    public WorkspaceV2HttpGateway Gateway { get; }
    public WorkspaceV2SidecarCapabilities? Capabilities { get; private set; }
    internal string DataDirectory => _dataDirectory;
    internal IReadOnlyDictionary<string, string> SidecarEnvironment { get; }
    internal IReadOnlyDictionary<string, string> BackendEnvironment { get; }

    public async Task StartAsync(
        WorkspaceOpenMode mode,
        WorkspaceActivationBudget budget)
    {
        ArgumentNullException.ThrowIfNull(budget);
        ObjectDisposedException.ThrowIf(
            Volatile.Read(ref _disposed) != 0,
            this);
        if (Interlocked.CompareExchange(ref _started, 1, 0) != 0)
            throw new InvalidOperationException(
                "Workspace runtime is already started.");
        try
        {
            await budget.RunAsync(
                WorkspaceActivationStage.Manifest,
                _ =>
                {
                    VerifyManifestBinding();
                    return Task.CompletedTask;
                }).ConfigureAwait(false);
            await _runtime.StartAsync(budget).ConfigureAwait(false);
        }
        catch
        {
            Volatile.Write(ref _started, 0);
            throw;
        }
    }

    public async Task VerifyAsync(WorkspaceActivationBudget budget)
    {
        ArgumentNullException.ThrowIfNull(budget);
        if (Volatile.Read(ref _started) == 0)
            throw new InvalidOperationException(
                "Workspace runtime has not started.");
        WorkspaceV2SidecarCapabilities? capabilities = null;
        await budget.RunAsync(
            WorkspaceActivationStage.Verification,
            async token =>
            {
                VerifyManifestBinding();
                capabilities = await Gateway.GetCapabilitiesAsync(token)
                    .ConfigureAwait(false);
            }).ConfigureAwait(false);
        if (capabilities is null)
            throw new InvalidOperationException("Workspace capabilities were not verified.");
        if (capabilities.ContractVersion != WorkspaceV2Json.ContractVersion
            || capabilities.WorkspaceId
                != WorkspaceId.ToString("D").ToLowerInvariant()
            || capabilities.SessionEpoch != SessionEpoch
            || capabilities.FenceEpoch != _authority.FenceEpoch
            || capabilities.ClaimId
                != _authority.ClaimId.ToString("D").ToLowerInvariant())
        {
            throw new WorkspaceRegistryException(
                "workspace.runtime_identity_mismatch",
                "The running workspace services do not match the requested session.");
        }
        Capabilities = capabilities;
        _owner.Activate(this);
    }

    public async Task DrainAsync(CancellationToken cancellationToken)
    {
        if (Volatile.Read(ref _started) == 0)
            throw new InvalidOperationException(
                "Workspace runtime has not started.");
        await _runtime.StopIngressAsync(cancellationToken)
            .ConfigureAwait(false);
        _ = await Gateway.DrainAsync(
                TimeSpan.FromSeconds(30),
                cancellationToken)
            .ConfigureAwait(false);
        _owner.Deactivate(this);
    }

    public async Task ResumeAsync(
        WorkspaceOpenMode mode,
        CancellationToken cancellationToken)
    {
        if (Volatile.Read(ref _started) == 0)
            throw new InvalidOperationException(
                "Workspace runtime has already stopped.");
        await _runtime.ResumeIngressAsync(cancellationToken)
            .ConfigureAwait(false);
        _owner.Activate(this);
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        _owner.Deactivate(this);
        if (Interlocked.Exchange(ref _started, 0) == 0)
            return;
        await _runtime.StopAsync(cancellationToken).ConfigureAwait(false);
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
            return;
        _owner.Deactivate(this);
        _runtime.ClientReady -= OnClientReady;
        _runtime.RecoveryFailed -= OnRecoveryFailed;
        Gateway.Dispose();
        await _runtime.DisposeAsync().ConfigureAwait(false);
    }

    private void VerifyManifestBinding()
    {
        string root = ProductionWorkspaceRuntimeFactory.RuntimeRoot(Workspace);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(root);
        if (manifest.WorkspaceId != WorkspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Runtime root contains a different workspace UUID.");
        WorkspacePaths paths = WorkspaceLayout.Paths(root);
        string expectedData = Path.GetFullPath(paths.Data);
        if (!string.Equals(
                expectedData,
                _dataDirectory,
                StringComparison.OrdinalIgnoreCase))
        {
            throw new WorkspaceRegistryException(
                "workspace.runtime_root_mismatch",
                "Runtime data root does not match the workspace activity root.");
        }
    }

    private void OnClientReady()
    {
        if (Capabilities is not null)
            _owner.NotifyClientReady(this);
    }

    private void OnRecoveryFailed(Exception exception)
        => _owner.NotifyRecoveryFailed(this, exception);
}

internal sealed class DesktopWorkspaceAuthorityStore
{
    private const int FormatVersion = 1;
    private readonly object _gate = new();

    public DesktopWorkspaceAuthority Prepare(
        WorkspaceRegistryEntryV2 workspace)
    {
        lock (_gate)
        {
            DesktopWorkspaceAuthority? current = TryRead(workspace);
            if (current is not null)
                return current;
            string root =
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
            WorkspacePaths paths = WorkspaceLayout.Paths(root);
            if (File.Exists(Path.Combine(
                    paths.Coordination,
                    "write-coordinator.db")))
                throw new WorkspaceRegistryException(
                    "workspace.authority_missing",
                    "Workspace authority metadata is missing for an existing coordinator.");
            var prepared = new DesktopWorkspaceAuthority(
                FormatVersion,
                workspace.WorkspaceId,
                1,
                Guid.NewGuid(),
                0);
            Write(workspace, prepared);
            return prepared;
        }
    }

    public DesktopWorkspaceAuthority Reserve(
        WorkspaceRegistryEntryV2 workspace,
        ulong sessionEpoch)
    {
        if (sessionEpoch == 0 || sessionEpoch > long.MaxValue)
            throw new WorkspaceRegistryException(
                "workspace.session_epoch_invalid",
                "Workspace session epoch is invalid.");
        lock (_gate)
        {
            DesktopWorkspaceAuthority? current = TryRead(workspace);
            if (current is not null
                && sessionEpoch <= current.LastSessionEpoch)
            {
                throw new WorkspaceRegistryException(
                    "workspace.session_epoch_stale",
                    "Workspace session epoch did not advance.");
            }
            string root =
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
            WorkspacePaths paths = WorkspaceLayout.Paths(root);
            string coordinator = Path.Combine(
                paths.Coordination,
                "write-coordinator.db");
            if (current is null && File.Exists(coordinator))
            {
                throw new WorkspaceRegistryException(
                    "workspace.authority_missing",
                    "Workspace authority metadata is missing for an existing coordinator.");
            }
            var next = current is null
                ? new DesktopWorkspaceAuthority(
                    FormatVersion,
                    workspace.WorkspaceId,
                    1,
                    Guid.NewGuid(),
                    sessionEpoch)
                : current with { LastSessionEpoch = sessionEpoch };
            Write(workspace, next);
            return next;
        }
    }

    public DesktopWorkspaceAuthority? TryRead(
        WorkspaceRegistryEntryV2 workspace)
    {
        string path = PathFor(workspace);
        if (!File.Exists(path))
            return null;
        try
        {
            DesktopWorkspaceAuthority authority =
                JsonSerializer.Deserialize<DesktopWorkspaceAuthority>(
                    File.ReadAllText(path, Encoding.UTF8),
                    WorkspaceV2Json.StrictOptions)
                ?? throw new JsonException("Authority file is empty.");
            if (authority.FormatVersion != FormatVersion
                || authority.WorkspaceId != workspace.WorkspaceId
                || authority.FenceEpoch == 0
                || authority.ClaimId == Guid.Empty
                )
            {
                throw new JsonException("Authority file is invalid.");
            }
            return authority;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or JsonException)
        {
            throw new WorkspaceRegistryException(
                "workspace.authority_corrupt",
                "Workspace authority metadata could not be read.",
                exception);
        }
    }

    private static void Write(
        WorkspaceRegistryEntryV2 workspace,
        DesktopWorkspaceAuthority authority)
    {
        string path = PathFor(workspace);
        string directory = Path.GetDirectoryName(path)!;
        Directory.CreateDirectory(directory);
        string temporary = Path.Combine(
            directory,
            $".desktop-runtime-authority.{Guid.NewGuid():N}.tmp");
        try
        {
            using (var stream = new FileStream(
                       temporary,
                       FileMode.CreateNew,
                       FileAccess.Write,
                       FileShare.None,
                       4096,
                       FileOptions.WriteThrough))
            {
                JsonSerializer.Serialize(
                    stream,
                    authority,
                    WorkspaceV2Json.StrictOptions);
                stream.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            if (File.Exists(temporary))
                File.Delete(temporary);
        }
    }

    private static string PathFor(WorkspaceRegistryEntryV2 workspace)
        => Path.Combine(
            WorkspaceLayout.Paths(
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace))
                .Coordination,
            "desktop-runtime-authority.json");
}

internal sealed record DesktopWorkspaceAuthority(
    int FormatVersion,
    Guid WorkspaceId,
    ulong FenceEpoch,
    Guid ClaimId,
    ulong LastSessionEpoch);
