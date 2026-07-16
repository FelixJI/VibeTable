using System.IO;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class HandshakeFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void HandshakeResponseFixture_Deserializes_ProtocolVersion_AndCapabilities()
    {
        var fixturePath = Path.Combine(
            AppContext.BaseDirectory,
            "fixtures",
            "system-handshake-response.json");

        var json = File.ReadAllText(fixturePath);
        var response = JsonSerializer.Deserialize<RpcResponse<HandshakeResult>>(json, Options);

        Assert.IsNotNull(response);
        Assert.AreEqual("1.0", response!.Result!.ProtocolVersion);
        CollectionAssert.Contains(response.Result.Capabilities, "table.read");
    }

    [TestMethod]
    public void HandshakeRequestFixture_Deserializes_HandshakeParams()
    {
        var fixturePath = Path.Combine(
            AppContext.BaseDirectory,
            "fixtures",
            "system-handshake-request.json");

        var json = File.ReadAllText(fixturePath);
        var request = JsonSerializer.Deserialize<RpcRequest<HandshakeParams>>(json, Options);

        Assert.IsNotNull(request);
        Assert.AreEqual("system.handshake", request!.Method);
        Assert.AreEqual("1.0", request.Params.ProtocolVersion);
    }
}
