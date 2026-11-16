import { FormField, Input, Select, Textarea } from "../../shell/ui";
import { AUDIT_RULE_TYPES, type AuditRuleType, type AuditSpec } from "./api/client";
import styles from "./pages.module.css";

export const ruleLabel: Record<AuditRuleType, string> = {
  DUPLICATE: "Duplicate vendor invoice",
  KEYWORD: "Keyword",
  LIST_SEARCH: "Watch list",
  SAMPLING: "Sampling",
  THRESHOLD: "Amount above limit",
  WEEKEND_HOLIDAY: "Weekend or holiday",
  AGGREGATE: "Monthly total above limit",
};

export const ruleHelp: Record<AuditRuleType, string> = {
  DUPLICATE: "Same vendor and currency, amount within 0.5% (at least 0.01), dated within 30 days, invoice numbers equal or one character apart. The later invoice is flagged.",
  KEYWORD: "Flags documents whose text contains any of the words, ignoring case and punctuation.",
  LIST_SEARCH: "Flags documents whose party is on the watch list, for example employees set up as vendors.",
  SAMPLING: "Picks documents for review by a seeded draw; the same seed always picks the same documents.",
  THRESHOLD: "Flags documents above the limit for their category, in the limit currency.",
  WEEKEND_HOLIDAY: "Flags documents dated on a Saturday, Sunday or a listed holiday (UTC day).",
  AGGREGATE: "Flags every document of a party whose monthly total is above the limit.",
};

const DECIMAL = /^\d+(\.\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

function list(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

/** The rule settings as the form holds them: text for every field of every rule type. */
export interface SpecValues {
  type: AuditRuleType;
  words: string;
  currency: string;
  limit: string;
  limits: string;
  holidays: string;
  seed: string;
  sampleBy: "percent" | "everyK";
  sampleSize: string;
}

export type SpecField = Exclude<keyof SpecValues, "type" | "sampleBy">;
export type SpecErrors = Partial<Record<SpecField, string>>;

export const emptySpec: SpecValues = { type: "DUPLICATE", words: "", currency: "", limit: "", limits: "", holidays: "", seed: "", sampleBy: "percent", sampleSize: "" };

/** Checks the rule settings and builds the spec; amounts stay decimal strings. */
export function toAuditSpec(v: SpecValues): { spec?: AuditSpec; errors: SpecErrors } {
  const errors: SpecErrors = {};
  const spec: AuditSpec = { type: v.type };
  const currency = v.currency.trim().toUpperCase();
  switch (v.type) {
    case "KEYWORD":
    case "LIST_SEARCH": {
      const words = list(v.words);
      if (words.length === 0) errors.words = v.type === "KEYWORD" ? "Enter at least one keyword." : "Enter at least one watch-list entry.";
      if (v.type === "KEYWORD") spec.keywords = words;
      else spec.watchList = words;
      break;
    }
    case "THRESHOLD": {
      if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
      const limits: Record<string, string> = {};
      for (const [i, line] of v.limits.split("\n").entries()) {
        if (!line.trim()) continue;
        const [category = "", amount = ""] = line.split("=").map((s) => s.trim());
        if (!category || !DECIMAL.test(amount) || Number(amount) <= 0) {
          errors.limits = `Line ${i + 1}: write "CATEGORY = positive amount".`;
          break;
        }
        limits[category] = amount;
      }
      if (!errors.limits && Object.keys(limits).length === 0) errors.limits = "Enter a limit for at least one category.";
      spec.currency = currency;
      spec.limits = limits;
      break;
    }
    case "AGGREGATE":
      if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
      if (!DECIMAL.test(v.limit.trim()) || Number(v.limit) <= 0) errors.limit = "Enter a positive monthly limit.";
      spec.currency = currency;
      spec.limit = v.limit.trim();
      break;
    case "WEEKEND_HOLIDAY": {
      const holidays = list(v.holidays);
      const bad = holidays.find((h) => !DATE.test(h));
      if (bad) errors.holidays = `${bad} is not a YYYY-MM-DD date.`;
      if (holidays.length > 0) spec.holidays = holidays;
      break;
    }
    case "SAMPLING": {
      if (!v.seed.trim()) errors.seed = "Enter the seed stored with the run.";
      const n = Number(v.sampleSize.trim());
      const ok = Number.isInteger(n) && n >= 1 && (v.sampleBy === "everyK" || n <= 100);
      if (!ok) errors.sampleSize = v.sampleBy === "percent" ? "Enter a whole percentage from 1 to 100." : "Enter a whole number from 1.";
      spec.sample = v.sampleBy === "percent" ? { seed: v.seed.trim(), percent: n } : { seed: v.seed.trim(), everyK: n };
      break;
    }
    case "DUPLICATE":
      break;
  }
  return Object.keys(errors).length > 0 ? { errors } : { spec, errors };
}

/** The form values of a stored spec, to edit it or to show it. */
export function specToValues(spec: AuditSpec): SpecValues {
  return {
    type: spec.type,
    words: (spec.keywords ?? spec.watchList ?? []).join(", "),
    currency: spec.currency ?? "",
    limit: spec.limit ?? "",
    limits: Object.entries(spec.limits ?? {})
      .map(([category, amount]) => `${category} = ${amount}`)
      .join("\n"),
    holidays: (spec.holidays ?? []).join(", "),
    seed: spec.sample?.seed ?? "",
    sampleBy: spec.sample?.everyK ? "everyK" : "percent",
    sampleSize: String(spec.sample?.everyK ?? spec.sample?.percent ?? ""),
  };
}

/** The rule type and its settings; pass `idPrefix` to keep field ids unique on a page. */
export function SpecFields({ values, errors, set, idPrefix = "audit" }: { values: SpecValues; errors: SpecErrors; set<K extends keyof SpecValues>(name: K, value: SpecValues[K]): void; idPrefix?: string }) {
  const t = values.type;
  return (
    <>
      <FormField id={`${idPrefix}-type`} label="Rule" hint={ruleHelp[t]}>
        {(control) => (
          <Select {...control} value={t} onChange={(e) => set("type", e.target.value as AuditRuleType)}>
            {AUDIT_RULE_TYPES.map((r) => (
              <option key={r} value={r}>
                {ruleLabel[r]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {t === "KEYWORD" || t === "LIST_SEARCH" ? (
        <FormField id={`${idPrefix}-words`} label={t === "KEYWORD" ? "Keywords" : "Watch list"} hint="Separate entries with commas or new lines." error={errors.words}>
          {(control) => <Input {...control} value={values.words} onChange={(e) => set("words", e.target.value)} invalid={Boolean(errors.words)} />}
        </FormField>
      ) : null}
      {t === "THRESHOLD" || t === "AGGREGATE" ? (
        <FormField id={`${idPrefix}-currency`} label="Limit currency" error={errors.currency}>
          {(control) => <Input {...control} maxLength={3} value={values.currency} onChange={(e) => set("currency", e.target.value.toUpperCase())} invalid={Boolean(errors.currency)} />}
        </FormField>
      ) : null}
      {t === "AGGREGATE" ? (
        <FormField id={`${idPrefix}-limit`} label="Monthly limit per party" error={errors.limit}>
          {(control) => <Input {...control} inputMode="decimal" value={values.limit} onChange={(e) => set("limit", e.target.value)} invalid={Boolean(errors.limit)} />}
        </FormField>
      ) : null}
      {t === "THRESHOLD" ? (
        <div className={styles.wide}>
          <FormField id={`${idPrefix}-limits`} label="Limits by category" hint='One per line, for example "MEALS = 200".' error={errors.limits}>
            {(control) => <Textarea {...control} rows={3} mono value={values.limits} onChange={(e) => set("limits", e.target.value)} invalid={Boolean(errors.limits)} />}
          </FormField>
        </div>
      ) : null}
      {t === "WEEKEND_HOLIDAY" ? (
        <FormField id={`${idPrefix}-holidays`} label="Holidays" hint="YYYY-MM-DD, separated by commas." error={errors.holidays} optional>
          {(control) => <Input {...control} value={values.holidays} onChange={(e) => set("holidays", e.target.value)} invalid={Boolean(errors.holidays)} />}
        </FormField>
      ) : null}
      {t === "SAMPLING" ? (
        <>
          <FormField id={`${idPrefix}-seed`} label="Seed" hint="Stored with the policy so a rerun selects the same documents." error={errors.seed}>
            {(control) => <Input {...control} value={values.seed} onChange={(e) => set("seed", e.target.value)} invalid={Boolean(errors.seed)} />}
          </FormField>
          <FormField id={`${idPrefix}-sample-by`} label="Sample by">
            {(control) => (
              <Select {...control} value={values.sampleBy} onChange={(e) => set("sampleBy", e.target.value as SpecValues["sampleBy"])}>
                <option value="percent">Percentage of documents</option>
                <option value="everyK">Every k-th document</option>
              </Select>
            )}
          </FormField>
          <FormField id={`${idPrefix}-sample-size`} label={values.sampleBy === "percent" ? "Percentage" : "k"} error={errors.sampleSize}>
            {(control) => <Input {...control} inputMode="numeric" value={values.sampleSize} onChange={(e) => set("sampleSize", e.target.value)} invalid={Boolean(errors.sampleSize)} />}
          </FormField>
        </>
      ) : null}
    </>
  );
}
