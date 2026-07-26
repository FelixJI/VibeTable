using System;
using System.IO;
using System.Windows;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop;

internal sealed class PocketBaseAdminWindow : Window
{
    private readonly PocketBaseAdminContext _context;
    private readonly string _profileRoot;
    private readonly WebView2 _webView = new();
    private bool _initialized;

    internal PocketBaseAdminWindow(
        PocketBaseAdminContext context,
        string profileRoot)
    {
        _context = context ?? throw new ArgumentNullException(nameof(context));
        _profileRoot = profileRoot
            ?? throw new ArgumentNullException(nameof(profileRoot));

        Title = "VibeTable Data Management";
        Width = 1280;
        Height = 820;
        MinWidth = 900;
        MinHeight = 620;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Content = _webView;

        Loaded += OnLoaded;
        Closed += OnClosed;
    }

    internal Uri Origin => _context.Origin;

    private async void OnLoaded(object sender, RoutedEventArgs args)
    {
        if (_initialized) return;
        _initialized = true;

        try
        {
            Directory.CreateDirectory(_profileRoot);
            CoreWebView2Environment environment =
                await CoreWebView2Environment.CreateAsync(
                    browserExecutableFolder: null,
                    userDataFolder: _profileRoot);
            await _webView.EnsureCoreWebView2Async(environment);

            CoreWebView2 core = _webView.CoreWebView2
                ?? throw new InvalidOperationException(
                    "WebView2 initialization did not provide a browser core.");
            string resourceFilter =
                $"{_context.Origin.GetLeftPart(UriPartial.Authority)}/*";
            core.AddWebResourceRequestedFilter(
                resourceFilter,
                CoreWebView2WebResourceContext.All);
            core.WebResourceRequested += OnWebResourceRequested;
            core.NavigationStarting += OnNavigationStarting;
            core.NewWindowRequested += OnNewWindowRequested;
            _webView.Source = _context.BootstrapUri;
        }
        catch (Exception exception)
        {
            MessageBox.Show(
                this,
                $"Unable to open local data management.\n\n{exception.Message}",
                "VibeTable",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            Close();
        }
    }

    private void OnWebResourceRequested(
        object? sender,
        CoreWebView2WebResourceRequestedEventArgs args)
    {
        if (!IsSameOrigin(args.Request.Uri)) return;
        args.Request.Headers.SetHeader(
            _context.SessionHeaderName,
            _context.SessionSecret);
    }

    private void OnNavigationStarting(
        object? sender,
        CoreWebView2NavigationStartingEventArgs args)
    {
        if (!IsSameOrigin(args.Uri))
        {
            args.Cancel = true;
        }
    }

    private void OnNewWindowRequested(
        object? sender,
        CoreWebView2NewWindowRequestedEventArgs args)
    {
        args.Handled = true;
        if (IsSameOrigin(args.Uri))
        {
            _webView.Source = new Uri(args.Uri, UriKind.Absolute);
        }
    }

    private bool IsSameOrigin(string? rawUri)
    {
        if (!Uri.TryCreate(rawUri, UriKind.Absolute, out Uri? target))
        {
            return false;
        }

        return string.Equals(
                target.Scheme,
                _context.Origin.Scheme,
                StringComparison.OrdinalIgnoreCase)
            && string.Equals(
                target.Host,
                _context.Origin.Host,
                StringComparison.OrdinalIgnoreCase)
            && target.Port == _context.Origin.Port;
    }

    private async void OnClosed(object? sender, EventArgs args)
    {
        try
        {
            if (_webView.CoreWebView2 is not null)
            {
                await _webView.CoreWebView2.Profile.ClearBrowsingDataAsync(
                    CoreWebView2BrowsingDataKinds.AllProfile);
            }
        }
        catch
        {
            // Best-effort cleanup. The isolated profile is never shared.
        }
        finally
        {
            _webView.Dispose();
            try
            {
                Directory.Delete(_profileRoot, recursive: true);
            }
            catch
            {
                // WebView2 may release profile files shortly after disposal.
            }
        }
    }
}
