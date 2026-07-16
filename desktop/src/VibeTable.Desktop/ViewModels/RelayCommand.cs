using System;
using System.Windows.Input;

namespace VibeTable.Desktop.ViewModels;

/// <summary>
/// Minimal <see cref="ICommand"/> implementation that delegates
/// <c>Execute</c>/<c>CanExecute</c> to injected delegates and raises
/// <see cref="CanExecuteChanged"/> when the VM explicitly calls
/// <see cref="RaiseCanExecuteChanged"/> (e.g. after a state transition).
/// </summary>
/// <remarks>
/// <para>
/// The execute delegate may be synchronous (<see cref="Action"/>) or
/// asynchronous (<see cref="Func{TResult}"/> of <see cref="System.Threading.Tasks.Task"/>).
/// The async form is used by commands whose handler is itself async (e.g.
/// <c>RetryCommand</c>); the ICommand surface stays synchronous, and the
/// returned <see cref="System.Threading.Tasks.Task"/> is fire-and-forget from
/// the binding engine's point of view — but the handler runs its first
/// synchronous segment (state transitions driven by synchronous fakes) inline,
/// so unit tests that drive <c>Execute</c> against a synchronous fake observe
/// the post-transition state immediately.
/// </para>
/// </remarks>
internal sealed class RelayCommand : ICommand
{
    private readonly Action _executeSync;
    private readonly Func<bool> _canExecute;

    public RelayCommand(Action execute, Func<bool>? canExecute = null)
    {
        _executeSync = execute ?? throw new ArgumentNullException(nameof(execute));
        _canExecute = canExecute ?? (() => true);
    }

    public bool CanExecute(object? parameter) => _canExecute();

    public void Execute(object? parameter) => _executeSync();

    public event EventHandler? CanExecuteChanged;

    public void RaiseCanExecuteChanged()
        => CanExecuteChanged?.Invoke(this, EventArgs.Empty);
}
