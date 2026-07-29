using System.Windows;

namespace VibeTable.PreviewHost;

public partial class App : Application
{
    protected override void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);
        int exitCode = PreviewHostEntry.Start(this, e.Args);
        if (exitCode != 0)
        {
            Shutdown(exitCode);
        }
    }
}
