type Json = Record<string, unknown>;

const isObject = (v: unknown): v is Json =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const ID_KEY =
  /^(?:[iI][dD]s?|.*_[iI][dD]s?|.*[a-z](?:Ids?|IDs?)|.*[rR]efs?|.*[a-z]No|.*_no)$/;
const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const mentions = (text: string, id: string) =>
  new RegExp(`(?<![\\w-])${escapeRegExp(id)}(?![\\w-])`).test(text);

function matchesType(type: string, value: unknown): boolean {
  switch (type) {
    case "object":
      return isObject(value);
    case "array":
      return Array.isArray(value);
    case "string":
      return typeof value === "string";
    case "number":
      return typeof value === "number" && Number.isFinite(value);
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    default:
      return true;
  }
}

export function schemaViolations(
  schema: Json,
  value: unknown,
  path = "$",
): string[] {
  if (value === null && schema["nullable"] === true) return [];
  const types = schema["type"];
  if (types !== undefined) {
    const allowed = Array.isArray(types) ? types.map(String) : [String(types)];
    if (!allowed.some((t) => matchesType(t, value)))
      return [`${path} must be ${allowed.join(" or ")}`];
  }
  const out: string[] = [];
  const choices = schema["enum"];
  if (
    Array.isArray(choices) &&
    !choices.some((e) => JSON.stringify(e) === JSON.stringify(value))
  )
    out.push(`${path} is not one of the allowed values`);
  if (isObject(value)) {
    const required = schema["required"];
    if (Array.isArray(required)) {
      for (const key of required)
        if (typeof key === "string" && !Object.hasOwn(value, key))
          out.push(`${path}.${key} is required`);
    }
    const properties = schema["properties"];
    if (isObject(properties)) {
      for (const [key, sub] of Object.entries(properties))
        if (isObject(sub) && Object.hasOwn(value, key))
          out.push(...schemaViolations(sub, value[key], `${path}.${key}`));
    }
  }
  const items = schema["items"];
  if (Array.isArray(value) && isObject(items))
    value.forEach((item, i) =>
      out.push(...schemaViolations(items, item, `${path}[${i}]`)),
    );
  return out;
}

export function unresolvedIds(
  value: unknown,
  source: string,
  path = "$",
): string[] {
  const text = source.toLowerCase();
  if (Array.isArray(value))
    return value.flatMap((item, i) =>
      unresolvedIds(item, source, `${path}[${i}]`),
    );
  if (!isObject(value)) return [];
  return Object.entries(value).flatMap(([key, v]) => {
    const here = `${path}.${key}`;
    if (!ID_KEY.test(key)) return unresolvedIds(v, source, here);
    const candidates = Array.isArray(v) ? v : [v];
    const bad = candidates.flatMap((c, i) => {
      const at = Array.isArray(v) ? `${here}[${i}]` : here;
      if (isObject(c)) return unresolvedIds(c, source, at);
      const id = typeof c === "number" && Number.isFinite(c) ? String(c) : c;
      return typeof id === "string" &&
        id.trim() !== "" &&
        mentions(text, id.trim().toLowerCase())
        ? []
        : [at];
    });
    return Array.isArray(v) && v.length === 0 ? [here] : bad;
  });
}
