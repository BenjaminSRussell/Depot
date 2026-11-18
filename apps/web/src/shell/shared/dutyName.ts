export function dutyName(code: string): string {
  const parts = code.split(".").filter(Boolean);
  const verb = parts.pop() ?? code;
  const phrase = [verb, ...parts].join(" ").replace(/-/g, " ");
  return phrase.charAt(0).toUpperCase() + phrase.slice(1);
}
