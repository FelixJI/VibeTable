using System.Text.RegularExpressions;
using VibeTable.Contracts;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class ApplicationVersionTests
{
    [TestMethod]
    public void FromAssembly_ReturnsTheSharedSemanticVersionWithoutBuildMetadata()
    {
        string version = ApplicationVersion.FromAssembly(typeof(ApplicationVersion).Assembly);

        Assert.IsTrue(
            Regex.IsMatch(version, @"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$"),
            $"Expected a semantic application version, got {version}.");
        Assert.IsFalse(version.Contains('+', StringComparison.Ordinal));
    }
}
