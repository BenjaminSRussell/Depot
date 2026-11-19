export const SPEC_REF = /\b(?:FR|BR|AC)-\d+\.\d+/;
export const DELIVERY_REF = /\b(?:wave|slice)[ -]?\d+/i;
export const UUID_WORD = /\buuids?\b/i;
export const ID_PROMPT = /\b(?:enter|type|paste|provide)\s+(?:an?|the)\s+(?:[\w-]+\s+){0,3}?(?:id|identifier)\b/i;
export const PROBLEM_CODE = /\b[A-Z]{2,6}-\d{3,4}\b/;
export const RAW_ENUM = /\b[A-Z]{2,}(?:_[A-Z0-9]+)+\b/;

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

export function dutyCode(duties: readonly string[]): RegExp {
  return new RegExp(`(?<![\\w.-])(?:${duties.map(escape).join("|")})(?![\\w-])`);
}

export function plainRules(duties: readonly string[]): Record<string, RegExp> {
  return { SPEC_REF, DELIVERY_REF, UUID_WORD, ID_PROMPT, PROBLEM_CODE, RAW_ENUM, DUTY_CODE: dutyCode(duties) };
}
