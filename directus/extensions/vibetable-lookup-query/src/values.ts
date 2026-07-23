import type { LookupAggregate, OutputType } from "./contracts.ts";
import { LookupQueryError } from "./errors.ts";

interface DecimalParts {
  coefficient: bigint;
  scale: number;
}

function typeError(message: string): never {
  throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", message);
}

function pow10(scale: number): bigint {
  return 10n ** BigInt(scale);
}

function parseDecimal(value: unknown): DecimalParts {
  const text = typeof value === "number" && Number.isFinite(value) ? String(value) : value;
  if (typeof text !== "string" || !/^-?(?:0|[1-9]\d*)(?:\.\d+)?$/.test(text)) {
    typeError("a decimal lookup source returned a non-canonical decimal value");
  }
  const negative = text.startsWith("-");
  const unsigned = negative ? text.slice(1) : text;
  const [whole, fraction = ""] = unsigned.split(".");
  const coefficient = BigInt(`${negative ? "-" : ""}${whole}${fraction}`);
  return { coefficient, scale: fraction.length };
}

function rescale(value: DecimalParts, scale: number): bigint {
  if (value.scale === scale) return value.coefficient;
  if (value.scale < scale) return value.coefficient * pow10(scale - value.scale);
  const divisor = pow10(value.scale - scale);
  const quotient = value.coefficient / divisor;
  const remainder = value.coefficient % divisor;
  if (remainder === 0n) return quotient;
  const absolute = remainder < 0n ? -remainder : remainder;
  const away = absolute * 2n >= divisor;
  return quotient + (away ? (value.coefficient < 0n ? -1n : 1n) : 0n);
}

function formatDecimal(coefficient: bigint, scale: number): string {
  const negative = coefficient < 0n;
  let digits = (negative ? -coefficient : coefficient).toString();
  if (scale === 0) return `${negative ? "-" : ""}${digits}`;
  digits = digits.padStart(scale + 1, "0");
  const whole = digits.slice(0, -scale);
  const fraction = digits.slice(-scale);
  return `${negative ? "-" : ""}${whole}.${fraction}`;
}

function decimalSum(values: readonly unknown[], output: OutputType): string {
  const parsed = values.map(parseDecimal);
  const commonScale = Math.max(...parsed.map((value) => value.scale));
  const exact = parsed.reduce<bigint>((total, value) => total + rescale(value, commonScale), 0n);
  return formatDecimal(rescale({ coefficient: exact, scale: commonScale }, output.scale!), output.scale!);
}

function decimalAverage(values: readonly unknown[], output: OutputType): string {
  const parsed = values.map(parseDecimal);
  const commonScale = Math.max(...parsed.map((value) => value.scale));
  const exact = parsed.reduce<bigint>((total, value) => total + rescale(value, commonScale), 0n);
  const outputScale = output.scale!;
  const numerator = outputScale >= commonScale ? exact * pow10(outputScale - commonScale) : exact;
  const divisor = BigInt(values.length) * (outputScale >= commonScale ? 1n : pow10(commonScale - outputScale));
  const quotient = numerator / divisor;
  const remainder = numerator % divisor;
  const absolute = remainder < 0n ? -remainder : remainder;
  const rounded = quotient + (absolute * 2n >= divisor ? (numerator < 0n ? -1n : 1n) : 0n);
  return formatDecimal(rounded, outputScale);
}

export function normalizeScalar(value: unknown, output: OutputType): unknown {
  if (value === null || value === undefined) return null;
  switch (output.kind) {
    case "string":
    case "uuid":
      if (typeof value !== "string") typeError(`a ${output.kind} lookup source returned a non-string value`);
      return value;
    case "integer": {
      const integer = typeof value === "string" && /^-?\d+$/.test(value) ? Number(value) : value;
      if (!Number.isSafeInteger(integer)) typeError("an integer lookup source returned an unsafe integer");
      return integer;
    }
    case "decimal":
      return formatDecimal(rescale(parseDecimal(value), output.scale!), output.scale!);
    case "boolean":
      if (typeof value !== "boolean") typeError("a boolean lookup source returned a non-boolean value");
      return value;
    case "date":
      if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) typeError("a date lookup source returned a non-canonical date");
      return value;
    case "time":
      if (typeof value !== "string" || !/^\d{2}:\d{2}(?::\d{2}(?:\.\d{1,6})?)?$/.test(value)) typeError("a time lookup source returned a non-canonical local time");
      return value;
    case "datetime": {
      if (typeof value !== "string" || !/(?:Z|[+-]\d{2}:\d{2})$/.test(value)) typeError("a datetime lookup source must include an explicit offset");
      const time = Date.parse(value);
      if (!Number.isFinite(time)) typeError("a datetime lookup source returned an invalid value");
      return new Date(time).toISOString();
    }
    case "json":
      return value;
  }
}

function stableValue(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return `[${value.map(stableValue).join(",")}]`;
  if (typeof value === "object") {
    return `{${Object.entries(value as Record<string, unknown>)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, item]) => `${JSON.stringify(key)}:${stableValue(item)}`)
      .join(",")}}`;
  }
  return `${typeof value}:${String(value)}`;
}

export function compareValues(left: unknown, right: unknown, output?: OutputType): number {
  if (left === null || left === undefined) return right === null || right === undefined ? 0 : 1;
  if (right === null || right === undefined) return -1;
  if (typeof left === "object" || typeof right === "object") {
    return stableValue(left).localeCompare(stableValue(right));
  }
  if (output && output.kind !== "json") {
    left = normalizeScalar(left, output);
    right = normalizeScalar(right, output);
  }
  if (output?.kind === "decimal") {
    const scale = Math.max(parseDecimal(left).scale, parseDecimal(right).scale);
    const a = rescale(parseDecimal(left), scale);
    const b = rescale(parseDecimal(right), scale);
    return a < b ? -1 : a > b ? 1 : 0;
  }
  if (typeof left === "number" && typeof right === "number") return left - right;
  if (typeof left === "boolean" && typeof right === "boolean") return Number(left) - Number(right);
  return stableValue(left).localeCompare(stableValue(right));
}

export interface LookupValue {
  value: unknown;
  collection?: string;
  itemId?: unknown;
}

export function aggregateValues(
  inputs: readonly LookupValue[],
  aggregate: LookupAggregate,
  output: OutputType,
  structuredM2A = false,
): unknown {
  if (aggregate === "count") return inputs.length;
  if (aggregate === "count_non_null") return inputs.filter((input) => input.value !== null && input.value !== undefined).length;
  const rawNonNull = inputs.filter((input) => input.value !== null && input.value !== undefined);
  const normalized = inputs.map((input) => ({ ...input, value: normalizeScalar(input.value, output) }));
  const nonNull = normalized.filter((input) => input.value !== null);
  if (aggregate === "scalar") return normalized[0]?.value ?? null;
  if (aggregate === "list" || aggregate === "distinct") {
    let listed = normalized;
    if (aggregate === "distinct") {
      const seen = new Set<string>();
      listed = normalized.filter((input) => {
        const key = stableValue(structuredM2A ? input : input.value);
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      });
    }
    return structuredM2A
      ? listed.map((input) => ({ collection: input.collection, itemId: input.itemId, value: input.value }))
      : listed.map((input) => input.value);
  }
  if (nonNull.length === 0) return null;
  if (aggregate === "sum" || aggregate === "avg") {
    if (output.kind === "decimal") return aggregate === "sum" ? decimalSum(rawNonNull.map((item) => item.value), output) : decimalAverage(rawNonNull.map((item) => item.value), output);
    const values = nonNull.map((item) => normalizeScalar(item.value, output) as number);
    const result = values.reduce((sum, value) => sum + value, 0);
    const final = aggregate === "avg" ? result / values.length : result;
    if (!Number.isSafeInteger(final)) typeError("an integer aggregate exceeded the safe integer range or was fractional");
    return final;
  }
  const sorted = nonNull.map((item) => item.value).sort((a, b) => compareValues(a, b, output));
  return aggregate === "min" ? sorted[0] : sorted.at(-1);
}

export function canonicalKey(value: unknown): string {
  return stableValue(value);
}
