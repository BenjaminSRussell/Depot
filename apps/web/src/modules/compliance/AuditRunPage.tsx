import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, evaluateAuditPolicy, getAuditPolicy, type AuditDocument, type AuditEvaluateResult, type AuditFinding, type AuditPolicy, type Problem } from "./api/client";
import { ruleHelp, ruleLabel } from "./auditSpec";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const DECIMAL = /^\d+(\.\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;
/** Rules that read the document date; a document without one cannot be judged. */
const DATED_RULES = new Set(["DUPLICATE", "WEEKEND_HOLIDAY", "AGGREGATE"]);

/**
 * Parses one document per line, fields separated by "|":
 * id | party | category | amount | currency | YYYY-MM-DD | invoice number | text.
 * Trailing fields may be left out; blank lines are skipped.
 */
export function parseDocuments(text: string): { documents: AuditDocument[]; error?: string } {
  const documents: AuditDocument[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [id = "", party = "", category = "", amount = "", currency = "", date = "", number = "", ...rest] = lines[i].split("|").map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!id) return { documents: [], error: `${at}: start with the document id.` };
    if (seen.has(id)) return { documents: [], error: `${at}: document ${id} is listed twice.` };
    if (amount && !DECIMAL.test(amount)) return { documents: [], error: `${at}: the amount must be a number such as 1250.50.` };
    if (currency && !CURRENCY.test(currency)) return { documents: [], error: `${at}: the currency must be a three-letter code such as USD.` };
    if (date && (!DATE.test(date) || Number.isNaN(new Date(`${date}T00:00:00Z`).getTime()))) return { documents: [], error: `${at}: the date must be YYYY-MM-DD.` };
    seen.add(id);
    const doc: AuditDocument = { id };
    if (party) doc.partyId = party;
    if (category) doc.category = category;
    if (amount) doc.amount = amount;
    if (currency) doc.currency = currency;
    if (date) doc.date = `${date}T00:00:00Z`;
    if (number) doc.number = number;
    const body = rest.join("|").trim();
    if (body) doc.texts = [body];
    documents.push(doc);
  }
  return { documents };
}

function evidenceText(f: AuditFinding): string {
  const e = f.evidence ?? {};
  switch (f.ruleType) {
    case "DUPLICATE":
      return `Repeats ${e.duplicateOf ?? "an earlier invoice"}`;
    case "KEYWORD":
      return `Mentions ${e.keywords ?? ""}`;
    case "LIST_SEARCH":
      return `Party ${e.partyId ?? ""} is on the watch list`;
    case "THRESHOLD":
      return `${e.amount} against a ${e.category} limit of ${e.limit} (${e.pctOfLimit}%)`;
    case "WEEKEND_HOLIDAY":
      return `Dated ${e.date}`;
    case "AGGREGATE":
      return `${e.partyId} totals ${e.total} in ${e.period}`;
    case "SAMPLING":
      return "Selected for review";
  }
  return Object.entries(e)
    .map(([k, val]) => `${k}: ${val}`)
    .join(", ");
}

const columns: DataTableColumn<AuditFinding>[] = [
  { key: "documentId", header: "Document", mono: true },
  { key: "evidence", header: "Why", render: (f) => evidenceText(f) },
  { key: "score", header: "Score", align: "end" },
  { key: "key", header: "Violation key", render: (f) => <span className={`${styles.mono} ${styles.muted}`}>{f.key}</span> },
];


/** Checks the documents against the rule the policy holds. */
export function toRunInput(policyId: string, policy: AuditPolicy | null, documentsText: string): { input?: { policyId: string; documents: AuditDocument[] }; errors: { policyId?: string; documents?: string } } {
  const errors: { policyId?: string; documents?: string } = {};
  if (policyId === "") errors.policyId = "Choose the audit policy.";
  const parsed = parseDocuments(documentsText);
  if (parsed.error) errors.documents = parsed.error;
  else if (policy && DATED_RULES.has(policy.spec.type)) {
    const undated = parsed.documents.find((d) => !d.date);
    if (undated) errors.documents = `Document ${undated.id} needs a date for this rule.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { input: { policyId, documents: parsed.documents }, errors };
}

/**
 * Audit rule run (PRD §9.48.6 Audit policies, duty compliance.audit-policy.run): runs a stored audit policy over a
 * document window and lists the violations it would create (FR-48.14). Each violation key is fixed by policy, version and
 * document, so a rerun creates no new violations. Nothing is stored yet.
 */
export function AuditRunPage() {
  const [policyId, setPolicyId] = useState("");
  const [policy, setPolicy] = useState<AuditPolicy | null>(null);
  const [documents, setDocuments] = useState("");
  const [errors, setErrors] = useState<{ policyId?: string; documents?: string }>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<AuditEvaluateResult | null>(null);

  async function choose(id: string) {
    setPolicyId(id);
    setPolicy(null);
    setResult(null);
    if (id === "") return;
    try {
      setPolicy(await getAuditPolicy(id));
    } catch (error) {
      setFailure(describeProblem(error));
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toRunInput(policyId, policy, documents);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await evaluateAuditPolicy(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const summary = result
    ? result.count === 0
      ? "No document violates this rule."
      : `${result.count} ${result.count === 1 ? "document violates" : "documents violate"} the policy (${result.code ?? "AUD-0701"}).`
    : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run an audit rule"
        description="Check a window of expenses, vendor invoices, purchase orders or journals against one audit policy and see the violations it would raise. Nothing is saved yet."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Audit rule">
          <LookupFormField
            id="audit-policy"
            label="Audit policy"
            entity="auditPolicy"
            allowCreate
            hint={policy ? `${ruleLabel[policy.spec.type]}, version ${policy.version}. ${ruleHelp[policy.spec.type]}` : "The rule to run. Policies are kept under Audit policies."}
            error={errors.policyId}
            value={policyId}
            onChange={(id) => void choose(id)}
          />
          <div className={styles.wide}>
            <FormField
              id="audit-documents"
              label="Documents"
              hint="One per line: id | party | category | amount | currency | YYYY-MM-DD | invoice number | text. Trailing fields may be left out."
              error={errors.documents}
              optional
            >
              {(control) => <Textarea {...control} rows={6} mono value={documents} onChange={(e) => setDocuments(e.target.value)} invalid={Boolean(errors.documents)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run rule
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The rule could not be run" : undefined} live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone={result && result.count > 0 ? "warning" : "success"} live="status">
        {summary}
      </Alert>
      {result ? (
        <DataTable
          label="Audit violations"
          columns={columns}
          rows={result.findings}
          getRowKey={(f) => f.key}
          empty={{ title: "No violations", description: "Every document passes this rule." }}
        />
      ) : (
        <Card>
          <EmptyState icon="info" title="No run yet" description="Choose a policy, paste the documents to check and run it." compact />
        </Card>
      )}
    </div>
  );
}
