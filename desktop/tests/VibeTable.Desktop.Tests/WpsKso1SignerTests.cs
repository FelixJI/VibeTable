using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// KSO-1 签名官方示例向量回放：
/// https://open.wps.cn/documents/app-integration-dev/wps365/server/api-description/signature-description
/// </summary>
[TestClass]
public sealed class WpsKso1SignerTests
{
    private const string AccessKey = "AK123456";
    private const string SecretKey = "sk098765";

    [TestMethod]
    public void OfficialExampleEmptyBodyVectorMatches()
    {
        string signature = WpsKso1Signer.Sign("GET", "/v7/test?key=value", "application/json",
            "Mon, 02 Jan 2006 15:04:05 GMT", SecretKey, []);
        Assert.AreEqual(
            "ce8df66877175e5198c8ea1362ffddf82e4941c6f25a4ca205a1ad09d0faaf03",
            signature, "官方示例 1：空请求体签名字符串。");
        Assert.AreEqual("KSO-1 AK123456:ce8df66877175e5198c8ea1362ffddf82e4941c6f25a4ca205a1ad09d0faaf03",
            WpsKso1Signer.FormatAuthorization(AccessKey, signature));
    }

    [TestMethod]
    public void OfficialExampleJsonBodyVectorMatches()
    {
        // 官方示例 2 的请求体含空格：{"key": "value"}，签名对字节敏感，保持原文。
        byte[] body = "{\"key\": \"value\"}"u8.ToArray();
        string signature = WpsKso1Signer.Sign("POST", "/v7/test/body", "application/json",
            "Mon, 02 Jan 2006 15:04:05 GMT", SecretKey, body);
        Assert.AreEqual(
            "c46e6c988130818ecba2484d51ac685948fbbef6814602c7874d6bfc41dc17b3",
            signature, "官方示例 2：JSON 请求体签名字符串。");
    }

    [TestMethod]
    public void DateHeaderIsRfc1123Gmt()
    {
        string date = WpsKso1Signer.FormatDate(new DateTimeOffset(2006, 1, 2, 15, 4, 5, TimeSpan.Zero));
        Assert.AreEqual("Mon, 02 Jan 2006 15:04:05 GMT", date);
    }
}
