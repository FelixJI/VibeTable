using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRpcErrorMapperTests
{
    [TestMethod]
    public void MapsFieldPathAndDropsProviderSecrets()
    {
        JsonElement source = JsonDocument.Parse(
            """
            {"code":"schema.field.invalid_constraint",
             "path":"fields[2].constraints.scale",
             "message":"scale 不能大于 precision",
             "retryable":false,
             "details":{"sessionSecret":"must-not-cross","precision":8}}
            """).RootElement.Clone();

        Assert.IsTrue(ProductRpcErrorMapper.TryMap(source, out var response));
        var error = response.GetProperty("error");
        Assert.AreEqual(
            "fields[2].constraints.scale",
            error.GetProperty("path").GetString());
        Assert.IsFalse(response.GetRawText().Contains("sessionSecret"));
        Assert.AreEqual(
            8,
            error.GetProperty("details").GetProperty("precision").GetInt32());
    }

    [TestMethod]
    public void MapsGlobalErrorWithNullPath()
    {
        JsonElement source = JsonDocument.Parse(
            """
            {"code":"mutation.digest_conflict","path":null,
             "message":"record changed","details":{"recordId":"row-1"},
             "retryable":false}
            """).RootElement.Clone();

        Assert.IsTrue(ProductRpcErrorMapper.TryMap(source, out var response));
        var error = response.GetProperty("error");
        Assert.AreEqual("", error.GetProperty("path").GetString());
        Assert.AreEqual(
            "row-1",
            error.GetProperty("details").GetProperty("recordId").GetString());
    }

    [TestMethod]
    public void RejectsMalformedOrProviderNamedErrors()
    {
        JsonElement malformed = JsonDocument.Parse("""{"message":"bad"}""").RootElement.Clone();
        JsonElement provider = JsonDocument.Parse(
            """{"code":"pocketbase.failed","path":"","message":"bad"}""").RootElement.Clone();

        Assert.IsFalse(ProductRpcErrorMapper.TryMap(malformed, out _));
        Assert.IsFalse(ProductRpcErrorMapper.TryMap(provider, out _));
    }
}
