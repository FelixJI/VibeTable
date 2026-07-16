using System;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusSupervisorTests
{
    [TestMethod]
    public void Constructor_RejectsNullOptions()
    {
        Assert.Throws<ArgumentNullException>(
            () => new DirectusSupervisor(null!));
    }

    [TestMethod]
    public void Constructor_RejectsEmptyLocalDirectusDirectory()
    {
        var options = new DirectusLaunchOptions
        {
            LocalDirectusDirectory = string.Empty,
        };
        Assert.Throws<ArgumentException>(
            () => new DirectusSupervisor(options));
    }

    [TestMethod]
    public void Constructor_AcceptsValidOptions_StartsInStoppedState()
    {
        var options = new DirectusLaunchOptions
        {
            LocalDirectusDirectory = "C:\\directus",
        };
        var supervisor = new DirectusSupervisor(options);

        Assert.AreEqual(DirectusState.Stopped, supervisor.State);
        Assert.IsNull(supervisor.BaseUrl);
    }

    private static void AssertThrows<TException>(Action action)
        where TException : Exception
    {
        try
        {
            action();
        }
        catch (TException)
        {
            return;
        }
        catch (Exception ex)
        {
            Assert.Fail($"Expected {typeof(TException).Name}, got {ex.GetType().Name}: {ex.Message}");
        }
        Assert.Fail($"Expected {typeof(TException).Name}, but no exception was thrown.");
    }
}
