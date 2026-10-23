import type { ProductErrorPayload } from "@/contracts";

export class ProductRpcError extends Error {
  public override readonly name = "ProductRpcError";

  public constructor(
    public readonly code: string,
    public readonly path: string,
    message: string,
    public readonly details: Readonly<Record<string, unknown>> | null,
    public readonly retryable: boolean,
  ) {
    super(message);
  }
}

/**
 * The bridge resolved a Product RPC reply that is neither a legal
 * `ProductErrorPayload` envelope nor a success value matching the DTO
 * contract of the issued method. Replies that end here were answered by an
 * untrusted shape: the request outcome is unknown. Callers must not report
 * the operation as rejected or rolled back, must not resubmit it
 * automatically, and should re-read the authoritative state instead.
 */
export class ProductRpcProtocolError extends Error {
  public override readonly name = "ProductRpcProtocolError";

  public constructor(
    public readonly reason: "malformed-error-envelope" | "invalid-success-payload",
    message: string,
  ) {
    super(message);
  }
}

/** Returns the decoded value, or null when the value violates the DTO contract. */
export type ProductRpcSuccessDecoder<T> = (value: unknown) => T | null;

export function unwrapProductRpcResult<T>(
  value: unknown,
  decode: ProductRpcSuccessDecoder<T>,
  method: string,
): T {
  if (isErrorEnvelope(value)) {
    const error = productError(value);
    if (error) throw error;
    throw new ProductRpcProtocolError(
      "malformed-error-envelope",
      `Product RPC "${method}" replied with an error envelope that is not a legal ProductErrorPayload; the request outcome is unknown. Re-read the authoritative state instead of retrying.`,
    );
  }
  const decoded = decode(value);
  if (decoded === null) {
    throw new ProductRpcProtocolError(
      "invalid-success-payload",
      `Product RPC "${method}" replied with a success value that violates its DTO contract; the request outcome is unknown. Re-read the authoritative state instead of retrying.`,
    );
  }
  return decoded;
}

function isErrorEnvelope(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value) && "error" in value;
}

function productError(value: Record<string, unknown>): ProductRpcError | null {
  const candidate = value.error;
  if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) return null;
  const payload = candidate as Partial<ProductErrorPayload> & {
    readonly details?: Readonly<Record<string, unknown>> | null;
  };
  if (
    typeof payload.code !== "string"
    || typeof payload.path !== "string"
    || typeof payload.message !== "string"
  ) return null;
  // Optional fields follow the ProductErrorPayload contract exactly: omitted
  // or null details, and omitted retryable, are legal; any other type for
  // these fields is an untrusted envelope, not a normalizable domain error.
  if (payload.details !== undefined && payload.details !== null
    && (typeof payload.details !== "object" || Array.isArray(payload.details))) return null;
  if (payload.retryable !== undefined && typeof payload.retryable !== "boolean") return null;
  return new ProductRpcError(
    payload.code,
    payload.path,
    payload.message,
    payload.details ?? null,
    payload.retryable === true,
  );
}
