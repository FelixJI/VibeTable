using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop;

/// <summary>
/// PocketBase-only desktop composition root. Provider lifecycle, credentials,
/// and filesystem capabilities remain in native services; the renderer sees
/// only the closed product contracts.
/// </summary>
public partial class MainWindow : Window
{
    internal static readonly TimeSpan WorkspaceSessionShutdownTimeout =
        BackendLaunchOptions.DefaultStopTimeout
        + PocketBaseLaunchOptions.DefaultStopTimeout
        + TimeSpan.FromSeconds(2);

    private readonly ProductionWorkspaceRuntimeFactory _runtime;
    private readonly WebMessageRouter _router;
    private readonly ProductWebViewBridge _webBridge;
    private readonly ShellBootstrap _shellBootstrap;
    private readonly WorkspaceRegistry _workspaceRegistry;
    private readonly WorkspaceSessionManager _workspaceSessions;
    private readonly WorkspaceSessionEnvelopeFilter _workspaceSessionFilter;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly WorkspaceRepositoryOnboardingService _repositoryOnboarding;
    private readonly WorkspaceReplicaRecoveryService _replicaRecovery;
    private readonly IWorkspaceRepositoryRecoveryUi _repositoryRecoveryUi;
    private readonly WorkspacePathGrantStore _workspacePathGrants;
    private readonly WorkspaceProductController _workspaceProduct;
    private readonly IDatabasePicker _databasePicker;
    private readonly LazyProductTableGateway _tableGateway;
    private readonly TableWorkspaceService _workspace;
    private readonly GridStateCoordinator _coordinator;
    private readonly ProductWorkspaceController _productWorkspace;
    private readonly WorkspaceRequestDispatcher _dispatcher;
    private readonly IProductSidecarGatewayLifecycle _productSidecarGatewayLifecycle;
    private readonly PluginProjectContextBindingRegistry _databaseOpens;
    private readonly ProductAuthorityTransitionCoordinator _authorityTransition;
    private readonly DocumentRequestController _documentRequests;
    private readonly NativeProductFileRequestController _nativeProductFiles;
    private readonly PluginSurfaceSessionManager _pluginSurfaces;
    private readonly PluginWebViewResourceHost _pluginResources;
    private readonly PluginRequestDispatcher _pluginDispatcher;
    private readonly ApplicationRequestController _applicationRequests;
    private readonly HostRequestDispatcher _hostRequests;
    private readonly ILocalDocumentPreview _attachmentPreview;
    private readonly string _attachmentPreviewRoot;
    private readonly string _productDataRoot;
    private readonly string _activityRootBase;
    private readonly MainWindowViewModel _viewModel;
    private readonly TestModeReadinessWriter? _readiness;
    private readonly IUpdateActivationSettlement? _updateActivation;
    private readonly UpdateActivationWorkspaceHealthGate? _updateWorkspaceHealth;
    private readonly string? _e2eControlsDir;
    private readonly AppPreferencesService _appPreferencesService;
    private readonly TestModeHostController? _testModeHost;
    private readonly CancellationTokenSource _session = new();

    private JsonRpcProductDataGateway? _productGateway;
    private JsonRpcPluginGateway? _pluginGateway;
    private WorkspaceDocumentOsAdapter? _documentWorkspace;
    private ShellBootstrapResult? _shellBootstrapResult;
    private TrayIconController? _trayIcon;
    private bool _explicitExitRequested;
    private int _closing;
    private int _rendererBootstrapCompleted;
    private int _updateHealthProbeInProgress;
    private int _updateHealthProbeStarted;
    private bool _startHidden;

    /// <summary>
    /// True when this instance was launched via <c>--autostart</c> by a user
    /// who has enabled tray behavior, so <see cref="App"/> should hide the
    /// window into the tray right after showing it. The composition root
    /// derives this once from <see cref="HostStartupOptions"/> and the
    /// startup preferences; it never changes for a given instance.
    /// </summary>
    internal bool StartHidden => _startHidden;

    internal static IWorkspacePathPicker CreateWorkspacePathPicker(
        HostStartupOptions startup) =>
        startup.TestMode && !string.IsNullOrWhiteSpace(startup.E2eControlsDir)
            ? new TestModeWorkspacePathPicker(startup.E2eControlsDir)
            : new WindowsWorkspacePathPicker();

    public MainWindow()
        : this(null)
    {
    }

    internal MainWindow(IUpdateActivationSettlement? updateActivation)
    {
        _updateActivation = updateActivation;
        InitializeComponent();

        HostStartupOptions startup = HostStartupOptions.Current();
        _e2eControlsDir = startup.TestMode
            && !string.IsNullOrWhiteSpace(startup.E2eControlsDir)
                ? Path.GetFullPath(startup.E2eControlsDir)
                : null;
        _readiness = startup.TestMode
            ? new TestModeReadinessWriter(startup.ReadinessDir)
            : null;
        string? runtimeDataRoot = null;
        bool developmentDataRootRequested =
            !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot);
        if (startup.TestMode && developmentDataRootRequested)
        {
            throw new InvalidOperationException(
                "--dev-data-root cannot be combined with --test-mode.");
        }
        if (startup.TestMode && !string.IsNullOrWhiteSpace(startup.ReadinessDir))
        {
            runtimeDataRoot = Path.GetFullPath(
                Path.Combine(startup.ReadinessDir, "local-data"));
        }
        else if (developmentDataRootRequested)
        {
            runtimeDataRoot = Path.GetFullPath(startup.DevelopmentDataRoot!);
        }
        string localAppData = Environment.GetFolderPath(
            Environment.SpecialFolder.LocalApplicationData);
        _productDataRoot = runtimeDataRoot
            ?? Path.Combine(localAppData, "VibeTable", "shell");
        WindowsStartupRegistration? windowsStartupRegistration = null;
        _appPreferencesService = new AppPreferencesService(
            new JsonAppPreferencesStore(
                Path.Combine(_productDataRoot, "app-preferences.json")),
            startup.TestMode
                ? (IStartupRegistration)new InMemoryStartupRegistration()
                : (windowsStartupRegistration = WindowsStartupRegistration.ForCurrentProcess()));
        AppPreferences startupPreferences = _appPreferencesService.ReadForStartup();
        if (startup.TestModeTrayLifecycle)
        {
            // Persist inside the isolated test-mode data root so the renderer's
            // normal appPreferences.get refresh cannot undo the tray policy
            // before the black-box close request arrives.
            startupPreferences = _appPreferencesService.Update(
                new AppPreferencesPatch(true, null));
        }
        // Only an auto-started launch reconciles the startup value: a stale
        // pointer left after moving/reinstalling is cleaned so the next boot
        // re-points at the current process (or drops the value entirely).
        // ReconcileForCurrentProcess swallows registry failures itself.
        if (startup.AutoStart && windowsStartupRegistration is not null)
        {
            windowsStartupRegistration.ReconcileForCurrentProcess();
        }
        var releaseUpdateCoordinator = new ReleaseUpdateCoordinator(
            AppContext.BaseDirectory,
            ApplicationVersion.FromAssembly(typeof(MainWindow).Assembly),
            installationEnabled: !developmentDataRootRequested && !startup.TestMode);
        _activityRootBase = runtimeDataRoot is null
            ? Path.Combine(localAppData, "VibeTable", "activity")
            : Path.Combine(runtimeDataRoot, "activity");
        _attachmentPreviewRoot = Path.Combine(
            _productDataRoot,
            "attachment-preview",
            $"p{Environment.ProcessId}-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_attachmentPreviewRoot);
        _attachmentPreview = new ShellDocumentPreview();

        _pluginSurfaces = new PluginSurfaceSessionManager();
        _pluginResources = new PluginWebViewResourceHost(
            new PluginResourceHost(),
            _pluginSurfaces);
        _router = new WebMessageRouter(OnRoutedWebRequest);
        _webBridge = new ProductWebViewBridge(
            this,
            AppWebView,
            _router,
            _pluginResources,
            _readiness,
            OnRendererProcessFailed,
            runtimeDataRoot is not null
                ? Path.Combine(runtimeDataRoot, "webview2-user-data")
                : null,
            stableIsolatedUserDataRoot: runtimeDataRoot is not null);
        _workspaceRegistry = new WorkspaceRegistry(
            startup.TestMode ? runtimeDataRoot : null);
        IReadOnlyList<WorkspaceRegistryEntryV2> knownWorkspaces;
        try
        {
            knownWorkspaces = _workspaceRegistry.List();
        }
        catch (WorkspaceRegistryException)
        {
            knownWorkspaces = [];
        }
        Func<PocketBaseLaunchOptions> sidecarOptionsFactory =
            () => PocketBaseHostOptions.Resolve(
                AppContext.BaseDirectory,
                Environment.GetFolderPath(
                    Environment.SpecialFolder.LocalApplicationData));
        _runtime = new ProductionWorkspaceRuntimeFactory(
            sidecarOptionsFactory,
            () => BackendLaunchOptions.ResolveForHost(),
            knownWorkspaces);
        _repositoryOnboarding = new WorkspaceRepositoryOnboardingService(
            sidecarOptionsFactory,
            _runtime.PrepareRepositoryOnboarding);
        _replicaRecovery = new WorkspaceReplicaRecoveryService(
            sidecarOptionsFactory,
            _runtime);
        _repositoryRecoveryUi = new WorkspaceRepositoryRecoveryUi();
        _providerPolicy = WorkspaceProviderPolicy.Load(AppContext.BaseDirectory);
        _workspacePathGrants = new WorkspacePathGrantStore(
            CreateWorkspacePathPicker(startup));
        _shellBootstrap = new ShellBootstrap(_workspaceRegistry, _webBridge);
        WorkspaceSessionEnvelopeFilter? productionEnvelopeFilter = null;
        _workspaceSessions = new WorkspaceSessionManager(
            _workspaceRegistry,
            _runtime,
            new SidecarWorkspaceProtectionHook(
                _runtime,
                (workspaceId, sessionEpoch) =>
                    productionEnvelopeFilter?.ReserveHostSequence(
                        workspaceId,
                        sessionEpoch)
                    ?? throw new InvalidOperationException(
                        "Workspace request envelope filter is not bound.")),
            new WorkspaceCoordinationLeaseHook(),
            new WorkspaceReplicaPreOpenHook(
                _replicaRecovery,
                _repositoryOnboarding,
                _repositoryRecoveryUi),
            activationTrace: message => _readiness?.Trace(message));
        _workspaceSessionFilter = new WorkspaceSessionEnvelopeFilter(
            _workspaceSessions);
        productionEnvelopeFilter = _workspaceSessionFilter;
        _workspaceSessions.SetRequestDrainHook(_workspaceSessionFilter);
        var snapshotPackages = new SnapshotPackageBroker(
            sidecarOptionsFactory,
            () => BackendLaunchOptions.ResolveForHost(),
            _providerPolicy,
            _workspaceRegistry,
            _workspaceSessions,
            _productDataRoot);
        var workspaceStorage = new WorkspaceStorageBroker(
            _workspaceRegistry,
            _workspaceSessions,
            _providerPolicy,
            _productDataRoot,
            replicas: _replicaRecovery);
        var workspaceReply = new WorkspaceProductReplySink(_webBridge);
        var workspaceHost = new WorkspaceProductHost(
                () => _router.IsReady,
                () => Volatile.Read(ref _closing) != 0,
                () => _documentWorkspace is not null,
                action => { _ = Dispatcher.BeginInvoke(action); },
                OpenProductWorkspaceWhenReady,
                message => _readiness?.WriteError(message));
        var workspaceSession = new WorkspaceProductSessionPort(
            _runtime,
            _workspaceSessions,
            _workspaceSessionFilter);
        _updateWorkspaceHealth = updateActivation is null
            ? null
            : new UpdateActivationWorkspaceHealthGate(
                _workspaceRegistry,
                workspaceSession,
                new CurrentRuntimeUpdateWorkspaceSchemaReader(_runtime, _workspaceSessionFilter),
                reportReady: receipt => _readiness?.WriteUpdateReady(receipt),
                reportFailure: exception => _readiness?.WriteError(
                    "Post-update workspace health probe failed: "
                    + $"{exception.GetType().Name}: {exception.Message}"));
        var storageMeter = new WorkspaceStorageMeter();
        var bootstrap = new WorkspaceBootstrapPublisher(
            workspaceReply,
            workspaceHost,
            workspaceSession,
            _workspaceRegistry,
            _providerPolicy,
            storageMeter);
        var registryTopology = new WorkspaceRegistryTopologyController(
            workspaceSession,
            _workspaceRegistry,
            _providerPolicy,
            new WorkspaceRepositoryOnboardingPort(_repositoryOnboarding),
            _repositoryRecoveryUi,
            _replicaRecovery,
            _workspacePathGrants,
            _productDataRoot,
            _activityRootBase);
        var replicaStatus = new WorkspaceReplicaStatusController(
            workspaceReply,
            workspaceHost,
            workspaceSession,
            _workspaceRegistry,
            new WorkspaceReplicaStatusQuery(workspaceSession),
            bootstrap);
        _workspaceProduct = new WorkspaceProductController(
            workspaceReply,
            workspaceHost,
            workspaceSession,
            registryTopology,
            replicaStatus,
            bootstrap,
            _workspacePathGrants,
            snapshotPackages,
            workspaceStorage,
            () => _session.Token);
        _databasePicker = new SessionProductSourcePicker(_workspaceSessions);

        IPluginPackageSourcePicker pluginPackagePicker =
            _e2eControlsDir is null
                ? new WindowsPluginPackageSourcePicker()
                : new TestModePluginPackageSourcePicker(_e2eControlsDir);
        var productAuthority = new ProductAuthorityEpoch();
        _databaseOpens = new PluginProjectContextBindingRegistry(productAuthority);
        _pluginDispatcher = new PluginRequestDispatcher(
            _webBridge,
            _pluginSurfaces,
            pluginPackagePicker,
            _pluginResources,
            new WindowsPluginFilePicker(),
            new GitHubPluginPackageSource(
                Path.Combine(_productDataRoot, "plugin-downloads"),
                () => _appPreferencesService.Read()),
            message => _readiness?.Trace(message),
            () => PluginProjectContext.FromSession(_workspaceSessions.Current),
            productAuthority);
        var dailyQuotes = new DailyQuoteHostClient();
        _tableGateway = new LazyProductTableGateway(_workspaceSessionFilter);
        _workspace = new TableWorkspaceService(_tableGateway);
        ProductWorkspaceController? productWorkspace = null;
        _coordinator = new GridStateCoordinator(
            _tableGateway,
            notification => productWorkspace!.OnNotification(notification));
        _productWorkspace = new ProductWorkspaceController(
            _webBridge,
            _runtime,
            _workspaceSessions,
            _databasePicker,
            _workspace,
            _coordinator,
            () => _router.IsReady,
            () => Volatile.Read(ref _closing) != 0,
            () => _productGateway is not null,
            message => _readiness?.Trace(message),
            typeof(MainWindow).Assembly.GetName().Version?.ToString()
                ?? "unknown",
            authority: productAuthority,
            databaseOpens: _databaseOpens);
        productWorkspace = _productWorkspace;
        _workspace.Notification += _productWorkspace.OnNotification;
        _dispatcher = new WorkspaceRequestDispatcher(
            _workspace,
            _databasePicker,
            _webBridge,
            _coordinator,
            sessionEnvelopeFilter: _workspaceSessionFilter,
            pluginContext: () => PluginProjectContext.FromSession(_workspaceSessions.Current),
            authority: productAuthority,
            databaseOpens: _databaseOpens);
        _productSidecarGatewayLifecycle =
            new ProductSidecarGatewayLifecycle(_runtime, _dispatcher);
        _runtime.RegisterProductSidecarGatewayLifecycle(
            _productSidecarGatewayLifecycle);
        _authorityTransition = new ProductAuthorityTransitionCoordinator(
            productAuthority,
            _dispatcher.RetireDatabaseOpensAfterAuthorityTransition,
            _pluginDispatcher.SetProjectContextAfterAuthorityTransition,
            _dispatcher.PostRetiredDatabaseOpenCancellations);
        _documentRequests = new DocumentRequestController(
            _webBridge,
            () => _documentWorkspace,
            () => _webBridge.CurrentNativeFilePaths?.ToArray() ?? [],
            BeginDocumentDragOut,
            () => _session.Token);
        _nativeProductFiles = new NativeProductFileRequestController(
            _webBridge,
            new ProductFileRpcGatewayAdapter(() => _productGateway),
            new WindowsNativeProductFileHost(
                this,
                () => _webBridge.CurrentNativeFilePaths?.ToArray() ?? [],
                _e2eControlsDir,
                _attachmentPreviewRoot,
                _attachmentPreview),
            () => _session.Token,
            message => _readiness?.Trace(message));
        _applicationRequests = new ApplicationRequestController(
            _webBridge,
            new ApplicationRequestHost(
                ApplyAppPreferences,
                () => _ = EnsureTrayIcon(),
                RequestExit,
                message => _readiness?.Trace(message)),
            _appPreferencesService,
            releaseUpdateCoordinator,
            dailyQuotes,
            startupPreferences,
            () => _session.Token);
        _hostRequests = new HostRequestDispatcher(
            _webBridge,
            new HostLifecycleRequestController(
                _webBridge,
                new HostLifecycleActions(
                    OnRendererReady,
                    RequestExit,
                    RetryStartup,
                    OpenPocketBaseAdmin,
                    HandleDiagnosticsGetAsync)),
            _workspaceProduct,
            _applicationRequests,
            _documentRequests,
            _nativeProductFiles,
            _pluginDispatcher,
            _dispatcher,
            TraceHostRequest);

        _runtime.ClientReady += OnRuntimeClientReady;
        _runtime.RecoveryFailed += OnRuntimeRecoveryFailed;
        _runtime.BindingChanged += OnRuntimeBindingChanged;
        _workspaceSessions.Changed += OnWorkspaceSessionChanged;
        _viewModel = new MainWindowViewModel(_runtime, _webBridge);
        _viewModel.PropertyChanged += OnViewModelPropertyChanged;
        DataContext = _viewModel;

        Loaded += OnLoaded;
        Closing += OnClosing;
        Closed += OnClosed;
        Application.Current.SessionEnding += OnSessionEnding;
        ApplyAppPreferences(_applicationRequests.CurrentPreferences);
        // UI-driven E2E stays visible. The explicit tray-lifecycle submode is
        // allowed to exercise the production auto-start visibility policy;
        // its harness observes fixed host state files instead of clicking UI.
        _startHidden = (!startup.TestMode || startup.TestModeTrayLifecycle)
            && StartupVisibilityPolicy.ShouldStartHidden(
                startup.AutoStart,
                _applicationRequests.CurrentPreferences.MinimizeToTrayOnClose);
        if (_e2eControlsDir is not null)
        {
            _testModeHost = new TestModeHostController(
                _e2eControlsDir,
                new TestModeHost(
                    () => Volatile.Read(ref _closing) == 0
                        && !Dispatcher.HasShutdownStarted
                        && !Dispatcher.HasShutdownFinished,
                    action =>
                    {
                        _ = Dispatcher.BeginInvoke(
                            async () => await action().ConfigureAwait(true));
                    },
                    RequestExit,
                    Close,
                    async (workspaceId, cancellationToken) =>
                    {
                        _ = await _workspaceProduct.OpenAsync(
                            workspaceId,
                            WorkspaceOpenMode.Writable,
                            switching: false,
                            cancellationToken);
                    },
                    () => new TestModeHostState(
                        IsVisible,
                        _trayIcon?.Visible == true,
                        _workspaceSessions.Current),
                    message => _readiness?.Trace(message)),
                () => _session.Token);
        }
    }

    private async void OnLoaded(object sender, RoutedEventArgs args)
    {
        try
        {
            _readiness?.Trace("MainWindow: starting global shell");
            _shellBootstrapResult = await _shellBootstrap.StartAsync();
            _viewModel.MarkShellLoaded();
            _readiness?.Trace("MainWindow: loading shell renderer");
            await _viewModel.StartAsync();
            if (_viewModel.State == StartupState.Faulted)
            {
                Exception? error = _viewModel.LastStartupError;
                _webBridge.PostNotification(
                    "host.startupStateChanged",
                    new
                    {
                        phase = "faulted",
                        stage = "runtime",
                        detail = "全局界面加载失败，可重试。",
                        canRetry = true,
                        canCancel = false,
                    });
                _readiness?.WriteError(
                    error is null
                        ? "Product runtime startup failed."
                        : $"Product runtime startup failed: {error.GetType().Name}: {error.Message}");
            }
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"MainWindow: product runtime startup failed: {exception}");
            _readiness?.WriteError(
                $"Product runtime startup failed: {exception.GetType().Name}: " +
                exception.Message);
        }
    }

    private void OnRuntimeClientReady()
    {
        if (Volatile.Read(ref _closing) != 0) return;
        ProductSidecarGenerationSnapshot? productSidecarGeneration =
            _runtime.CaptureProductSidecarGeneration();
        if (productSidecarGeneration is not null)
        {
            _ = TryReplaceProductSidecarGatewayAsync(
                productSidecarGeneration);
        }
        _ = ConfigureRpcGatewaysAsync();
    }

    private async Task TryReplaceProductSidecarGatewayAsync(
        ProductSidecarGenerationSnapshot snapshot)
    {
        try
        {
            _ = await _productSidecarGatewayLifecycle.TryReplaceAsync(
                    snapshot,
                    _session.Token)
                .ConfigureAwait(true);
        }
        catch (OperationCanceledException) when (
            _session.IsCancellationRequested
            || Volatile.Read(ref _closing) != 0)
        {
        }
        catch (Exception exception)
        {
            const string code = "product_sidecar.gateway_binding_failed";
            _readiness?.Trace(
                $"{code}:{exception.GetType().Name}");
            _readiness?.WriteError(
                $"{code} ({exception.GetType().Name})");
        }
    }

    private async Task ConfigureRpcGatewaysAsync()
    {
        if (Volatile.Read(ref _closing) != 0) return;
        try
        {
            bool configured = await Dispatcher.InvokeAsync(TryConfigureRpcGateways)
                .Task
                .ConfigureAwait(true);
            if (!configured)
                throw new InvalidOperationException(
                    "ClientReady was published without a backend client.");
        }
        catch (Exception exception)
        {
            const string code = "backend.gateway_binding_failed";
            _readiness?.Trace($"{code}:{exception.GetType().Name}");
            _readiness?.WriteError($"{code} ({exception.GetType().Name})");
        }
    }

    private bool TryConfigureRpcGateways()
    {
        HostProductRpcBinding? binding = _runtime.CaptureHostProductRpcBinding();
        if (binding is null)
        {
            _tableGateway.Unbind();
            return false;
        }
        ConfigureRpcGateways(binding);
        return true;
    }

    private void ConfigureRpcGateways(HostProductRpcBinding binding)
    {
        JsonRpcClient client = binding.Client;
        _authorityTransition.Transition(null);
        _tableGateway.Bind(binding);

        if (_productGateway is not null)
        {
            _dispatcher.ClearProductDataGateway(_productGateway);
            _productGateway.DataChanged -= OnProductDataChanged;
            _productGateway.TaskChanged -= OnProductTaskChanged;
            _productGateway.Dispose();
        }
        _productGateway = binding.CreateGateway(_workspaceSessionFilter);
        _productGateway.DataChanged += OnProductDataChanged;
        _productGateway.TaskChanged += OnProductTaskChanged;
        _dispatcher.SetProductDataGateway(_productGateway);

        _dispatcher.SetDashboardGateway(
            new JsonRpcDashboardGateway(client),
            _session.Token);
        _dispatcher.SetSurfaceGateway(new JsonRpcSurfaceGateway(client));

        IPluginRpcGateway? previousPluginGateway = _pluginGateway;
        _pluginGateway = new JsonRpcPluginGateway(client);
        _pluginDispatcher.SetGatewayAfterAuthorityTransition(
            _pluginGateway,
            PluginProjectContext.FromSession(_workspaceSessions.Current));
        previousPluginGateway?.Dispose();

        _documentWorkspace?.Dispose();
        _documentWorkspace = new WorkspaceDocumentOsAdapter(
            CurrentWorkspaceDocumentBinding,
            new DocumentCapabilityStore(),
            new WindowsLocalDocumentActions(),
            new ShellDocumentPreview(),
            _e2eControlsDir is null
                ? new WindowsLocalDocumentFilePicker()
                : new TestModeLocalDocumentFilePicker(_e2eControlsDir),
            _workspaceSessionFilter,
            new OpenXmlDocumentDiffEngine(),
            Path.Combine(_productDataRoot, "document-diff"));
        _dispatcher.SetDocumentWorkspace(_documentWorkspace);
        _authorityTransition.Transition(
            PluginProjectContext.FromSession(_workspaceSessions.Current),
            _session.Token);

        _productWorkspace.ResetBinding();
        if (_router.IsReady
            && Volatile.Read(ref _rendererBootstrapCompleted) != 0
            && Volatile.Read(ref _updateHealthProbeInProgress) == 0)
        {
            PostRuntimeReady();
            OpenProductWorkspaceWhenReady();
        }
    }

    private WorkspaceDocumentBinding? CurrentWorkspaceDocumentBinding()
    {
        WorkspaceRegistryEntryV2? workspace = _runtime.CurrentWorkspace;
        WorkspaceV2HttpGateway? gateway = _runtime.CurrentV2Gateway;
        WorkspaceV2SidecarCapabilities? capabilities =
            _runtime.CurrentCapabilities;
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        if (workspace is null || gateway is null || capabilities is null ||
            session.WorkspaceId != workspace.WorkspaceId ||
            session.SessionEpoch == 0)
            return null;
        return new WorkspaceDocumentBinding(
            workspace.WorkspaceId,
            session.SessionEpoch,
            session.Writable,
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace),
            gateway,
            capabilities.RpcMethods);
    }

    private void PostRuntimeReady()
    {
        _webBridge.PostNotification(
            "host.startupStateChanged",
            new
            {
                phase = "ready",
                stage = "runtime",
                detail = "本地数据服务已就绪。",
                canRetry = false,
                canCancel = false,
            });
    }

    private void OnRuntimeRecoveryFailed(Exception exception)
    {
        _readiness?.WriteError(
            $"Local data recovery failed: {exception.GetType().Name}");
        Dispatcher.BeginInvoke(() =>
        {
            _webBridge.PostNotification(
                "host.startupStateChanged",
                new
                {
                    phase = "faulted",
                    stage = "runtime",
                    detail = "本地数据服务恢复失败，可重试或切换工作区。",
                    canRetry = true,
                    canCancel = false,
                });
            if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
            {
                _viewModel.MoveToFaulted("Local data recovery failed.");
            }
        });
    }

    private void OnWorkspaceSessionChanged(
        object? sender,
        WorkspaceSessionChangedEventArgs args)
    {
        if (Volatile.Read(ref _updateHealthProbeInProgress) != 0)
        {
            return;
        }
        PluginProjectContext? context = PluginProjectContext.FromSession(args.Session);
        _authorityTransition.Transition(context, _session.Token);
        if (context is null)
        {
            _webBridge.PostNotification(
                "plugin.projectContext.unavailable",
                new { reason = "workspace-session-unavailable" });
        }
        _workspaceProduct.OnSessionChanged(args);
    }

    private void OnRuntimeBindingChanged()
    {
        if (Volatile.Read(ref _closing) != 0)
            return;
        Dispatcher.BeginInvoke(() =>
        {
            HostProductRpcBinding? binding = _runtime.CaptureHostProductRpcBinding();
            if (binding is not null)
            {
                _authorityTransition.Transition(null);
                _tableGateway.Bind(binding);
                return;
            }
            _tableGateway.Unbind();
            _authorityTransition.Transition(null);
            _webBridge.PostNotification(
                "plugin.projectContext.unavailable",
                new { reason = "backend-binding-unavailable" });
            if (_productGateway is not null)
            {
                _dispatcher.ClearProductDataGateway(_productGateway);
                _productGateway.DataChanged -= OnProductDataChanged;
                _productGateway.TaskChanged -= OnProductTaskChanged;
                _productGateway.Dispose();
                _productGateway = null;
            }
            if (_pluginGateway is not null)
            {
                _pluginDispatcher.ClearGatewayAfterAuthorityTransition(_pluginGateway);
                _pluginGateway.Dispose();
            }
            _pluginGateway = null;
            _documentWorkspace?.Dispose();
            _documentWorkspace = null;
            _productWorkspace.ResetBinding();
        });
    }

    private void OnRendererProcessFailed(string reason)
    {
        _viewModel.MarkShellUnavailable();
        if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
        {
            _viewModel.MoveToFaulted(reason);
        }
    }

    private void OnRoutedWebRequest(RoutedWebRequest request) =>
        _hostRequests.Dispatch(request);

    private void TraceHostRequest(RoutedWebRequest request)
    {
        if (request.Type.StartsWith("file.", StringComparison.Ordinal))
        {
            _readiness?.Trace(
                $"Attachment request routed; type={request.Type}; " +
                $"requestIdPresent={!string.IsNullOrWhiteSpace(request.RequestId)}");
        }
    }

    private void OnRendererReady()
    {
        _router.IsReady = true;
        _dispatcher.RotateDocumentCapabilityEpoch();
        _pluginResources.CloseAllSurfaces();
        if (_updateWorkspaceHealth is null)
        {
            CompleteRendererBootstrap();
        }
        TryWriteReadiness();
    }

    private void CompleteRendererBootstrap()
    {
        if (Interlocked.Exchange(ref _rendererBootstrapCompleted, 1) != 0)
        {
            return;
        }
        _workspaceProduct.PostBootstrap();
        if (_runtime.CurrentBackend?.State == BackendState.Ready
            && _productGateway is not null)
        {
            PostRuntimeReady();
            OpenProductWorkspaceWhenReady();
        }
        else
        {
            _webBridge.PostNotification(
                "host.startupStateChanged",
                new
                {
                    phase = "starting",
                    stage = "shell",
                    detail = _shellBootstrapResult?.RegistryErrorCode is null
                        ? "工作区中心已就绪，请选择或创建工作区。"
                        : "工作区列表需要修复。",
                    canRetry = false,
                    canCancel = false,
                });
        }
    }

    private void RetryStartup()
    {
        Dispatcher.BeginInvoke(() =>
        {
            if (_viewModel.RetryCommand.CanExecute(null))
            {
                _viewModel.RetryCommand.Execute(null);
            }
        });
    }

    private void ApplyAppPreferences(AppPreferences preferences)
    {
        if (!preferences.MinimizeToTrayOnClose)
        {
            if (_trayIcon is not null) _trayIcon.Visible = false;
            return;
        }

        EnsureTrayIcon().Visible = true;
    }

    private TrayIconController EnsureTrayIcon() =>
        _trayIcon ??= new TrayIconController(RestoreFromTray, RequestExit);

    private void OnClosing(object? sender, CancelEventArgs args)
    {
        if (!WindowClosePolicy.ShouldMinimizeToTray(
                _applicationRequests.CurrentPreferences,
                _explicitExitRequested))
        {
            return;
        }

        ApplyAppPreferences(_applicationRequests.CurrentPreferences);
        args.Cancel = true;
        Hide();
    }

    private void RestoreFromTray()
    {
        Dispatcher.BeginInvoke(() =>
        {
            Show();
            if (WindowState == WindowState.Minimized)
            {
                WindowState = WindowState.Normal;
            }
            Activate();
        });
    }

    private void RequestExit()
    {
        Dispatcher.BeginInvoke(() =>
        {
            _explicitExitRequested = true;
            Close();
        });
    }

    internal void ReportTestModeStartupVisibility()
        => _testModeHost?.ReportStartupVisibility(StartHidden);

    private void OnSessionEnding(object? sender, SessionEndingCancelEventArgs args)
    {
        _explicitExitRequested = true;
    }

    private void OpenProductWorkspaceWhenReady()
        => _productWorkspace.OpenWhenReady();

    private void OnProductDataChanged(DataChangedEvent change)
    {
        _webBridge.PostNotification("data.changed", change);
    }

    private void OnProductTaskChanged(JsonElement change)
    {
        _webBridge.PostNotification("task.changed", change);
    }

    private void BeginDocumentDragOut(string path)
    {
        var data = new DataObject();
        data.SetData(DataFormats.FileDrop, new[] { path });
        DragDrop.DoDragDrop(AppWebView, data, DragDropEffects.Copy);
    }

    private void OnViewModelPropertyChanged(
        object? sender,
        PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(MainWindowViewModel.State))
        {
            if (_viewModel.State == StartupState.Ready)
            {
                _viewModel.DetailMessage = string.Empty;
            }
            TryWriteReadiness();
        }
    }

    private void TryWriteReadiness()
    {
        if (_viewModel.State != StartupState.Ready || !_router.IsReady)
        {
            return;
        }
        if (_updateActivation is null || _updateWorkspaceHealth is null)
        {
            _readiness?.WriteShellReady();
            return;
        }
        if (Interlocked.Exchange(ref _updateHealthProbeStarted, 1) != 0)
        {
            return;
        }
        Volatile.Write(ref _updateHealthProbeInProgress, 1);
        _ = ConfirmUpdateActivationAsync();
    }

    private async Task ConfirmUpdateActivationAsync()
    {
        try
        {
            _ = await _updateWorkspaceHealth!.ConfirmAsync(
                _updateActivation!,
                _session.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
            // The settlement receives Failed with CancellationToken.None before
            // this cancellation is rethrown, so rollback remains durable.
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Post-update workspace health probe failed: {exception}");
        }
        finally
        {
            bool probeSessionClosed =
                _workspaceSessions.Current.State == WorkspaceSessionState.Closed;
            if (probeSessionClosed)
            {
                Volatile.Write(ref _updateHealthProbeInProgress, 0);
                if (Volatile.Read(ref _closing) == 0)
                {
                    _ = Dispatcher.BeginInvoke(CompleteRendererBootstrap);
                }
            }
            else if (Volatile.Read(ref _closing) == 0)
            {
                // Keep the temporary runtime quarantined. CloseAsync restores
                // its read-only session after a failed stop, so normal product
                // bootstrap must not inherit that binding.
                _ = Dispatcher.BeginInvoke(() =>
                {
                    const string reason =
                        "更新后工作区健康探测无法关闭临时会话。";
                    _readiness?.Trace(reason);
                    _webBridge.PostNotification(
                        "host.startupStateChanged",
                        new
                        {
                            phase = "faulted",
                            stage = "runtime",
                            detail = reason,
                            canRetry = false,
                            canCancel = false,
                        });
                    if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
                    {
                        _viewModel.MoveToFaulted(reason);
                    }
                });
            }
        }
    }

    private void OnClosed(object? sender, EventArgs args)
    {
        if (Interlocked.Exchange(ref _closing, 1) != 0) return;
        _productSidecarGatewayLifecycle.Dispose();
        _testModeHost?.Dispose();
        Application.Current.SessionEnding -= OnSessionEnding;
        _trayIcon?.Dispose();
        _session.Cancel();
        _router.IsReady = false;
        _runtime.ClientReady -= OnRuntimeClientReady;
        _runtime.RecoveryFailed -= OnRuntimeRecoveryFailed;
        _runtime.BindingChanged -= OnRuntimeBindingChanged;
        _workspaceSessions.Changed -= OnWorkspaceSessionChanged;
        _viewModel.PropertyChanged -= OnViewModelPropertyChanged;
        if (_productGateway is not null)
        {
            _dispatcher.ClearProductDataGateway(_productGateway);
            _productGateway.DataChanged -= OnProductDataChanged;
            _productGateway.TaskChanged -= OnProductTaskChanged;
            _productGateway.Dispose();
        }
        _authorityTransition.Dispose();
        _databaseOpens.Dispose();
        _dispatcher.Dispose();
        _pluginDispatcher.Dispose();
        _applicationRequests.Dispose();
        _pluginGateway?.Dispose();
        _pluginResources.Dispose();
        _attachmentPreview.Dispose();
        _documentWorkspace?.Dispose();
        _workspace.Notification -= _productWorkspace.OnNotification;
        _productWorkspace.Dispose();
        _tableGateway.Dispose();
        try
        {
            _workspaceProduct.DisposeAsync().AsTask()
                .Wait(TimeSpan.FromSeconds(10));
        }
        catch
        {
            // Session polling and transient package cleanup are best-effort.
        }
        _workspaceSessionFilter.Dispose();
        try
        {
            _workspaceSessions.DisposeAsync().AsTask()
                .Wait(WorkspaceSessionShutdownTimeout);
        }
        catch
        {
            // Session teardown is best-effort during WPF shutdown.
        }
        try
        {
            _runtime.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(8));
        }
        catch
        {
            // Process-tree cleanup is best-effort during WPF shutdown.
        }
        _session.Dispose();
        AppWebView.Dispose();
        try
        {
            Directory.Delete(_attachmentPreviewRoot, recursive: true);
        }
        catch (IOException)
        {
            // A preview handler may briefly retain a file handle during exit.
        }
        catch (UnauthorizedAccessException)
        {
            // Best-effort cleanup of the per-process preview cache.
        }
    }

    private sealed class SessionProductSourcePicker(
        WorkspaceSessionManager sessions) : IDatabasePicker
    {
        public Task<string?> PickDatabaseAsync()
            => Task.FromResult(
                sessions.Current.WorkspaceId is Guid workspaceId
                    ? $"local://workspace/{workspaceId:D}"
                    : null);
    }
}
