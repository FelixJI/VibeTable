using System.ComponentModel;

namespace VibeTable.Desktop.ViewModels;

/// <summary>
/// Minimal <see cref="INotifyPropertyChanged"/> base for VMs. Avoids pulling
/// in a full MVVM framework for Phase A.
/// </summary>
public abstract class ViewModelBase : INotifyPropertyChanged
{
    public event PropertyChangedEventHandler? PropertyChanged;

    protected void RaisePropertyChanged(string propertyName)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(propertyName));
}
