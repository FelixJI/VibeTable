using System.Diagnostics;

namespace VibeTable.Desktop.Services;

public interface ILocalDocumentActions
{
    void Open(string fullPath);
    void Reveal(string fullPath);
}

/// <summary>Windows shell actions used only after capability and path checks.</summary>
public sealed class WindowsLocalDocumentActions : ILocalDocumentActions
{
    public void Open(string fullPath)
    {
        Process.Start(new ProcessStartInfo(fullPath)
        {
            UseShellExecute = true,
        });
    }

    public void Reveal(string fullPath)
    {
        var startInfo = new ProcessStartInfo("explorer.exe")
        {
            UseShellExecute = true,
        };
        startInfo.ArgumentList.Add("/select,");
        startInfo.ArgumentList.Add(fullPath);
        Process.Start(startInfo);
    }
}
