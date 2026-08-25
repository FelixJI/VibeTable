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

export function unwrapProductRpcResult<T>(value: unknown): T {
  const error = productError(value);
  if (error) throw error;
  return value as T;
}

function productError(value: unknown): ProductRpcError | null {
  if (!value || typeof value !== "object" || Array.isArray(value) || !("error" in value)) {
    return null;
  }
  const candidate = (value as { readonly error?: unknown }).error;
  if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) return null;
  const payload = candidate as Partial<ProductErrorPayload> & {
    readonly details?: Readonly<Record<string, unknown>> | null;
  };
  if (
    typeof payload.code !== "string"
    || typeof payload.path !== "string"
    || typeof payload.message !== "string"
  ) return null;
  return new ProductRpcError(
    payload.code,
    payload.path,
    payload.message,
    payload.details ?? null,
    payload.retryable === true,
  );
}
