using VibeTable.Desktop.Services;
namespace VibeTable.Desktop.Tests;
[TestClass]
public sealed class NativePluginFileDialogControlTests
{
    [TestMethod]
    [DataRow(null, "true")]
    [DataRow("true", null)]
    [DataRow("false", "true")]
    public void NativeDialogControlFailsClosedOutsideControlledCi(string? actions, string? ci)
        => Assert.ThrowsExactly<InvalidOperationException>(() => WindowsNativeProductFileHost.IsNativeDialogControl("native-dialog", actions, ci));
    [TestMethod]
    public void NativeDialogControlRequiresExactMarkerAndBothCiFlags()
    {
        Assert.IsTrue(WindowsNativeProductFileHost.IsNativeDialogControl("native-dialog", "true", "true"));
        Assert.IsFalse(WindowsNativeProductFileHost.IsNativeDialogControl("C:/synthetic.csv", null, null));
        Assert.IsFalse(WindowsNativeProductFileHost.IsNativeDialogControl("NATIVE-DIALOG", "true", "true"));
    }
}
