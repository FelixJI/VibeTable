namespace VibeTable.Contracts;

/// <summary>
/// Parameters for the <c>system.handshake</c> JSON-RPC method.
/// </summary>
public sealed record HandshakeParams(string ClientVersion, string ProtocolVersion);

/// <summary>
/// Result of the <c>system.handshake</c> JSON-RPC method. <see cref="Capabilities"/>
/// is the enumerable list of method names the backend supports.
/// </summary>
public sealed record HandshakeResult(
    string BackendVersion,
    string ProtocolVersion,
    string[] Capabilities);
