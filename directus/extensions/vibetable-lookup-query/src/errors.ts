export type LookupErrorCode =
  | "VIBETABLE_ACCOUNTABILITY_REQUIRED"
  | "VIBETABLE_LOOKUP_PLAN_INVALID"
  | "VIBETABLE_LOOKUP_UNSUPPORTED"
  | "VIBETABLE_LOOKUP_TOO_EXPENSIVE"
  | "VIBETABLE_LOOKUP_RESTRICTED"
  | "VIBETABLE_LOOKUP_SCHEMA_INVALID"
  | "VIBETABLE_LOOKUP_INTERNAL";

const STATUS: Record<LookupErrorCode, number> = {
  VIBETABLE_ACCOUNTABILITY_REQUIRED: 401,
  VIBETABLE_LOOKUP_PLAN_INVALID: 400,
  VIBETABLE_LOOKUP_UNSUPPORTED: 422,
  VIBETABLE_LOOKUP_TOO_EXPENSIVE: 422,
  VIBETABLE_LOOKUP_RESTRICTED: 403,
  VIBETABLE_LOOKUP_SCHEMA_INVALID: 409,
  VIBETABLE_LOOKUP_INTERNAL: 500,
};

export class LookupQueryError extends Error {
  public readonly code: LookupErrorCode;
  public readonly status: number;
  public readonly details?: Readonly<Record<string, unknown>>;

  public constructor(
    code: LookupErrorCode,
    message: string,
    details?: Readonly<Record<string, unknown>>,
  ) {
    super(message);
    this.name = "LookupQueryError";
    this.code = code;
    this.status = STATUS[code];
    this.details = details;
  }
}

export function errorResponse(error: unknown): {
  status: number;
  body: { errors: Array<{ message: string; extensions: Record<string, unknown> }> };
} {
  const safe = error instanceof LookupQueryError
    ? error
    : new LookupQueryError(
        "VIBETABLE_LOOKUP_INTERNAL",
        "lookup query failed",
      );
  return {
    status: safe.status,
    body: {
      errors: [
        {
          message: safe.message,
          extensions: {
            code: safe.code,
            ...(safe.details ? { details: safe.details } : {}),
          },
        },
      ],
    },
  };
}

export function directusReadError(
  error: unknown,
  details: Readonly<Record<string, unknown>>,
): LookupQueryError {
  const candidate = error as {
    status?: number;
    statusCode?: number;
    code?: string;
    extensions?: { code?: string };
  };
  const code = String(candidate?.extensions?.code ?? candidate?.code ?? "").toUpperCase();
  if (candidate?.status === 403 || candidate?.statusCode === 403 || code.includes("FORBIDDEN")) {
    return new LookupQueryError(
      "VIBETABLE_LOOKUP_RESTRICTED",
      "a lookup collection or field is restricted",
      details,
    );
  }
  return new LookupQueryError(
    "VIBETABLE_LOOKUP_SCHEMA_INVALID",
    "the lookup plan no longer matches the Directus schema",
    details,
  );
}
