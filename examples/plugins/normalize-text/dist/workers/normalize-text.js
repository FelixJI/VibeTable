export function normalizeText(value, strategy) {
  if (strategy === "trim") return value.trim();
  if (strategy === "collapse-whitespace") return value.trim().replace(/\s+/gu, " ");
  if (strategy === "lowercase") return value.toLocaleLowerCase();
  if (strategy === "uppercase") return value.toLocaleUpperCase();
  throw new Error(`unknown normalization strategy: ${strategy}`);
}
