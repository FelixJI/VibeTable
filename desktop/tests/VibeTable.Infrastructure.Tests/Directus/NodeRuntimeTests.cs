using System;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

/// <summary>
/// Tests for <see cref="NodeRuntime"/> path resolution. These cover the
/// file-system resolution logic (<see cref="NodeRuntime.ResolveBundledNode"/>)
/// which is the deterministic, side-effect-free part. The version-probing
/// <see cref="NodeRuntime.FindNode"/> spawns a real <c>node -v</c> process, so
/// it is exercised only against the repo's bundled Node (which is checked in
/// and therefore always present in a dev/test checkout).
/// </summary>
[TestClass]
public sealed class NodeRuntimeTests
{
    [TestMethod]
    public void ResolveBundledNode_ReturnsNullWhenBaseDirNull()
    {
        Assert.IsNull(NodeRuntime.ResolveBundledNode(null));
        Assert.IsNull(NodeRuntime.ResolveBundledNode(""));
        Assert.IsNull(NodeRuntime.ResolveBundledNode("   "));
    }

    [TestMethod]
    public void ResolveBundledNode_ReturnsNullWhenNodeExeAbsent()
    {
        WithTemporaryDirectory(root =>
        {
            // runtime/node/ exists but has no node.exe.
            Directory.CreateDirectory(Path.Combine(root, "runtime", "node"));
            Assert.IsNull(NodeRuntime.ResolveBundledNode(root));
        });
    }

    [TestMethod]
    public void ResolveBundledNode_ReturnsPathWhenNodeExePresent()
    {
        WithTemporaryDirectory(root =>
        {
            string nodeDir = Path.Combine(root, "runtime", "node");
            Directory.CreateDirectory(nodeDir);
            string nodeExe = Path.Combine(nodeDir, "node.exe");
            File.WriteAllText(nodeExe, "fake"); // contents irrelevant for resolution

            string? resolved = NodeRuntime.ResolveBundledNode(root);

            Assert.IsNotNull(resolved);
            Assert.AreEqual(nodeExe, resolved);
        });
    }

    [TestMethod]
    public void ResolveBundledNode_LooksUnderRuntimeNodeSubdir()
    {
        // Guards against the constant path drifting: the exe must be at
        // <base>/runtime/node/node.exe, not directly under <base>.
        WithTemporaryDirectory(root =>
        {
            // node.exe at the wrong location (root, not runtime/node/).
            File.WriteAllText(Path.Combine(root, "node.exe"), "fake");
            Assert.IsNull(NodeRuntime.ResolveBundledNode(root));
        });
    }

    private static void WithTemporaryDirectory(Action<string> body)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-node-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(root);
            body(root);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort cleanup */ }
        }
    }
}
