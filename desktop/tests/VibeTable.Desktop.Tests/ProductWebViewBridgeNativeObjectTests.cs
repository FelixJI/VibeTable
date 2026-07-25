using System;
using System.Collections.Generic;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWebViewBridgeNativeObjectTests
{
    [TestMethod]
    public void InspectNativeFileIngress_ConvertsAdditionalObjectsFailureToStableError()
    {
        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "file.uploadRequested",
                () => throw new InvalidOperationException("WebView2 getter failed"),
                _ => throw new AssertFailedException("path reader must not run"));

        Assert.AreEqual("NATIVE_OBJECTS_UNAVAILABLE", result.ErrorCode);
        Assert.AreEqual(
            "Native file objects could not be read by the desktop host.",
            result.ErrorMessage);
        Assert.IsNull(result.Paths);
    }

    [TestMethod]
    public void InspectNativeFileIngress_RejectsInvalidAdditionalObjectType()
    {
        object invalid = new();

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "document.externalDropRequested",
                () => new[] { invalid },
                _ => null);

        Assert.AreEqual("INVALID_NATIVE_OBJECT", result.ErrorCode);
        Assert.AreEqual(1, result.ObjectCount);
        Assert.IsNull(result.Paths);
    }

    [TestMethod]
    public void InspectNativeFileIngress_RejectsNativeObjectsForUnapprovedRequestType()
    {
        bool readerCalled = false;

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "dashboard.listRequested",
                () =>
                {
                    readerCalled = true;
                    return Array.Empty<object>();
                },
                _ => null);

        Assert.AreEqual("NATIVE_OBJECTS_NOT_ALLOWED", result.ErrorCode);
        Assert.IsFalse(readerCalled);
    }

    [TestMethod]
    public void InspectNativeFileIngress_ReturnsValidatedPaths()
    {
        object first = new();
        object second = new();
        var paths = new Dictionary<object, string>
        {
            [first] = @"C:\safe\one.txt",
            [second] = @"C:\safe\two.txt",
        };

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "file.replaceRequested",
                () => new[] { first, second },
                value => paths[value]);

        Assert.IsNull(result.ErrorCode);
        CollectionAssert.AreEqual(
            new[] { @"C:\safe\one.txt", @"C:\safe\two.txt" },
            result.Paths!.ToArray());
    }
}
