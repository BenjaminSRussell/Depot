import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  FormErrorSummary,
  FormField,
  FormSection,
  Input,
  PageHeader,
  Select,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
} from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { getGroup, listUnitTerms, setUnitTerms, type GroupDetail, type UnitTerms } from "./api/groups";
import { isDecimal, isPositive, isSignedDecimal } from "./format";
import styles from "./pages.module.css";

export interface TermsValues {
  investmentCost: string;
  shareCapital: string;
  preAcquisitionRe: string;
  historicalRate: string;
  method: "full" | "equity";
  sellerIsSubsidiary: boolean;
  icNetInvestment: boolean;
  fxGainLossAccount: string;
  icReceivableAccount: string;
  parentPayableAccount: string;
}

export const emptyTerms: TermsValues = {
  investmentCost: "",
  shareCapital: "",
  preAcquisitionRe: "",
  historicalRate: "1",
  method: "full",
  sellerIsSubsidiary: false,
  icNetInvestment: false,
  fxGainLossAccount: "",
  icReceivableAccount: "",
  parentPayableAccount: "",
};

const ACCOUNT = /^[A-Za-z0-9._:-]{1,64}$/;

export const termsFromStored = (t: UnitTerms): TermsValues => ({
  investmentCost: t.investmentCost,
  shareCapital: t.shareCapital,
  preAcquisitionRe: t.preAcquisitionRe,
  historicalRate: t.historicalRate,
  method: t.method ?? "full",
  sellerIsSubsidiary: t.sellerIsSubsidiary,
  icNetInvestment: t.icNetInvestment,
  fxGainLossAccount: t.fxGainLossAccount ?? "",
  icReceivableAccount: t.icReceivableAccount ?? "",
  parentPayableAccount: t.parentPayableAccount ?? "",
});

/** Checks the terms the way the service does and builds the request; amounts stay decimal strings. */
export function validateTerms(v: TermsValues): { errors: Record<string, string>; request?: Omit<UnitTerms, "unitId"> } {
  const errors: Record<string, string> = {};
  if (!isDecimal(v.investmentCost)) errors.investmentCost = "Enter the cost of the investment as an amount of zero or more, such as 1200000.00.";
  if (!isDecimal(v.shareCapital)) errors.shareCapital = "Enter the share capital at acquisition as an amount of zero or more.";
  if (!isSignedDecimal(v.preAcquisitionRe)) errors.preAcquisitionRe = "Enter the retained earnings at acquisition as an amount; a loss is negative.";
  if (!isPositive(v.historicalRate)) errors.historicalRate = "Enter the exchange rate of the acquisition day, above zero, such as 1.0840.";
  for (const [field, label] of [["fxGainLossAccount", "FX gain or loss account"], ["icReceivableAccount", "Intercompany receivable account"], ["parentPayableAccount", "Parent payable account"]] as const) {
    if (v[field].trim() !== "" && !ACCOUNT.test(v[field].trim())) errors[field] = `${label}: use letters, digits and . _ : - (up to 64).`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const optional = (value: string) => (value.trim() === "" ? undefined : value.trim());
  return {
    errors,
    request: {
      investmentCost: v.investmentCost.trim(),
      shareCapital: v.shareCapital.trim(),
      preAcquisitionRe: v.preAcquisitionRe.trim(),
      historicalRate: v.historicalRate.trim(),
      method: v.method,
      sellerIsSubsidiary: v.sellerIsSubsidiary,
      icNetInvestment: v.icNetInvestment,
      fxGainLossAccount: optional(v.fxGainLossAccount),
      icReceivableAccount: optional(v.icReceivableAccount),
      parentPayableAccount: optional(v.parentPayableAccount),
    },
  };
}

const FIELDS = {
  investmentCost: { id: "terms-investmentCost", label: "Cost of the investment" },
  shareCapital: { id: "terms-shareCapital", label: "Share capital at acquisition" },
  preAcquisitionRe: { id: "terms-preAcquisitionRe", label: "Retained earnings at acquisition" },
  historicalRate: { id: "terms-historicalRate", label: "Exchange rate on the acquisition day" },
  fxGainLossAccount: { id: "terms-fxGainLossAccount", label: "FX gain or loss account" },
  icReceivableAccount: { id: "terms-icReceivableAccount", label: "Intercompany receivable account" },
  parentPayableAccount: { id: "terms-parentPayableAccount", label: "Parent payable account" },
};

/**
 * The group's investment in one unit (acquisition terms, FR-40.7): what a consolidation run cannot read from a trial balance. Saving
 * replaces the earlier terms; the service keeps no version of them, so there is no If-Match.
 */
export function UnitTermsPage() {
  const { id = "", unitId = "" } = useParams();
  const navigate = useNavigate();
  const [group, setGroup] = useState<GroupDetail | null>(null);
  const [values, setValues] = useState<TermsValues>(emptyTerms);
  const [stored, setStored] = useState<boolean | null>(null);
  const [loadFailure, setLoadFailure] = useState<Problem | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));
  const loaded = useRef(false);

  useEffect(() => {
    const ctl = new AbortController();
    setLoadFailure(null);
    Promise.all([getGroup(id, ctl.signal), listUnitTerms(id, ctl.signal)])
      .then(([g, terms]) => {
        setGroup(g);
        const found = terms.items.find((t) => t.unitId === unitId);
        setStored(Boolean(found));
        if (found && !loaded.current) setValues(termsFromStored(found));
        loaded.current = true;
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setLoadFailure(describeProblem(err));
      });
    return () => ctl.abort();
  }, [id, unitId, reload]);

  const unit = group?.units.find((u) => u.id === unitId);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateTerms(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      await setUnitTerms(id, unitId, request);
      release();
      navigate(`/consolidation/groups/${id}`);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: keyof typeof FIELDS, extra: { hint?: string; mono?: boolean; optional?: boolean } = {}) => (
    <FormField id={FIELDS[name].id} label={FIELDS[name].label} hint={extra.hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          className={extra.mono === false ? undefined : styles.mono}
          value={values[name]}
          onChange={(e) => setValues({ ...values, [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  if (loadFailure) {
    return (
      <Alert tone="danger" title="We couldn't load these terms" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
        <ProblemMessage problem={loadFailure} />
      </Alert>
    );
  }
  if (!group || stored === null) return <p className={styles.note}>Loading the terms…</p>;
  if (!unit) {
    return (
      <Alert tone="danger" title="This unit is not in the group" live="alert" action={<Link to={`/consolidation/groups/${id}`}>Back to the group</Link>}>
        The unit was removed, or the link is wrong.
      </Alert>
    );
  }

  return (
    <div className={styles.stack}>
      {prompt}
      <PageHeader
        title="Acquisition terms"
        description={`The group's investment in a unit of ${group.name}. A consolidation run reads these figures for the elimination of the investment; without them it stops with CNS-0930. Saving replaces the earlier terms.`}
        actions={<Link to={`/consolidation/groups/${id}`}>Back to the group</Link>}
      />
      <Card>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Acquisition terms">
          <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
          <p className={styles.note}>
            Unit: <EntityName entity="legalEntity" id={unit.legalEntityId} />, ledger {unit.ledgerId}, {unit.currency}.
          </p>
          <FormField id="terms-method" label="Consolidation method" hint="Full consolidates the unit line by line; equity takes an associate with the equity method.">
            {(control) => (
              <Select {...control} value={values.method} onChange={(e) => setValues({ ...values, method: e.target.value as TermsValues["method"] })}>
                <option value="full">Full consolidation</option>
                <option value="equity">Equity method</option>
              </Select>
            )}
          </FormField>
          {text("investmentCost", { hint: `In the group currency (${group.currency}).` })}
          {text("shareCapital", { hint: `In the unit's currency (${unit.currency}).` })}
          {text("preAcquisitionRe", { hint: `In the unit's currency (${unit.currency}); a loss is negative.` })}
          {text("historicalRate", { hint: `Group currency per unit of ${unit.currency} on the day of acquisition.` })}
          <FormField id="terms-sellerIsSubsidiary" label="Intercompany goods flow">
            {(control) => (
              <Select {...control} value={values.sellerIsSubsidiary ? "up" : "down"} onChange={(e) => setValues({ ...values, sellerIsSubsidiary: e.target.value === "up" })}>
                <option value="down">The parent sells to this unit (downstream)</option>
                <option value="up">This unit sells to the parent (upstream)</option>
              </Select>
            )}
          </FormField>
          <FormField id="terms-icNetInvestment" label="Intercompany balance" hint="A balance that is part of the net investment in a foreign unit is translated into the translation reserve, not into profit or loss.">
            {(control) => (
              <Select {...control} value={values.icNetInvestment ? "yes" : "no"} onChange={(e) => setValues({ ...values, icNetInvestment: e.target.value === "yes" })}>
                <option value="no">Not part of the net investment</option>
                <option value="yes">Part of the net investment</option>
              </Select>
            )}
          </FormField>
          <FormSection collapse="advanced">
            {text("fxGainLossAccount", { optional: true, hint: "The group account that takes the exchange difference of the intercompany balance." })}
            {text("icReceivableAccount", { optional: true, hint: "The account in the unit's ledger holding its receivable from the holder; a run reads the intercompany receivable from it." })}
            {text("parentPayableAccount", { optional: true, hint: "The account in the holder's ledger holding the payable to this unit." })}
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The terms were not saved" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {stored ? "Save terms" : "Store terms"}
            </Button>
            <Link className={buttonClassName("ghost")} to={`/consolidation/groups/${id}`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
