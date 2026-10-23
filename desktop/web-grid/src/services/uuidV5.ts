// UUIDv5 (RFC 4122, SHA-1 over the URL namespace) derivation matching the Go
// producers' uuid.NewSHA1(uuid.NameSpaceURL, []byte(prefix + operationId)).

const NAMESPACE_URL_BYTES: readonly number[] = [
  0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1,
  0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
];

async function uuidV5(name: string): Promise<string> {
  const nameBytes = new TextEncoder().encode(name);
  const data = new Uint8Array(NAMESPACE_URL_BYTES.length + nameBytes.length);
  data.set(NAMESPACE_URL_BYTES);
  data.set(nameBytes, NAMESPACE_URL_BYTES.length);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-1", data));
  const bytes = digest.slice(0, 16);
  bytes[6] = (bytes[6] & 0x0f) | 0x50;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = [...bytes].map(byte => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** The logical preset id a fresh `preset.save` (no target) must be created with. */
export function derivePresetId(operationId: string): Promise<string> {
  return uuidV5(`vibetable:preset:${operationId}`);
}

/** The logical version id a `version.create` must be created with. */
export function deriveVersionId(operationId: string): Promise<string> {
  return uuidV5(`vibetable:version:${operationId}`);
}
