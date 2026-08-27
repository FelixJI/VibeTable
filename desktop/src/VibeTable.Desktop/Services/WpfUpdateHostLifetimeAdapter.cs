using System.Windows;

namespace VibeTable.Desktop.Services;

internal sealed class WpfUpdateHostLifetimeAdapter(Application application)
    : IUpdateHostLifetimePort
{
    private readonly Application _application = application
        ?? throw new ArgumentNullException(nameof(application));

    public void RequestExit(int exitCode)
    {
        _ = _application.Dispatcher.BeginInvoke(
            () => _application.Shutdown(exitCode));
    }
}
