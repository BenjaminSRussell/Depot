import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  FormErrorSummary,
  FormField,
  FormSection,
  Input,
  ListPage,
  loadedRows,
  PageHeader,
  Select,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  type ListColumn,
} from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { createCarrierService, describeProblem, listCarrierServices, LOAD_MODES, newKey, type CarrierService, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: ListColumn<CarrierService>[] = [
  { key: "carrierId", header: "Carrier", render: (row) => <EntityName entity="vendor" id={row.carrierId} /> },
  { key: "code", header: "Code", mono: true, sortValue: (row) => row.code },
  { key: "name", header: "Service", sortValue: (row) => row.name },
  { key: "mode", header: "Mode", sortValue: (row) => row.mode, render: (row) => <Badge tone="neutral">{row.mode}</Badge> },
  { key: "active", header: "Status", sortValue: (row) => (row.active ? 0 : 1), render: (row) => <Badge tone={row.active ? "success" : "neutral"}>{row.active ? "Active" : "Retired"}</Badge> },
];

/** The services each carrier sells; rate cards and rate shopping price them (BR-34.2). */
export function CarrierServicesPage() {
  const state = useLoad((signal) => listCarrierServices({}, signal), "carrier-services");
  return (
    <div className={styles.stack}>
      <ListPage
        id="transport-carrier-services"
        title="Carrier services"
        description="A service is one product a carrier sells, such as Ground or Next day air. A rate card prices one service of one carrier."
        noun="carrier services"
        singular="carrier service"
        columns={columns}
        useRows={() => loadedRows(state)}
        getRowKey={(row) => row.id}
        search={(row) => ({ name: row.name, number: row.code })}
        actions={
          <Link className={buttonClassName("primary")} to="/transport/carrier-services/new">
            New carrier service
          </Link>
        }
        empty={{ title: "No carrier services yet", description: "Add the services of each carrier before you add their rate cards." }}
      />
    </div>
  );
}

const FIELDS: Record<string, FormFieldTarget> = {
  carrierId: { id: "service-carrierId", label: "Carrier" },
  code: { id: "service-code", label: "Code" },
  name: { id: "service-name", label: "Name" },
  mode: { id: "service-mode", label: "Mode" },
};

/** Checks that mirror CarrierService.Validate in the service. */
export function validateService(v: { carrierId: string; code: string; name: string }): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!UUID.test(v.carrierId.trim())) errors.carrierId = "Choose the carrier.";
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(v.code.trim().toUpperCase())) errors.code = "Use 1 to 20 capital letters, digits, _ or -, such as GROUND.";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Name the service in up to 120 characters.";
  return errors;
}

/** Add a carrier service (transport.rate-card.approve). A code the carrier already uses is refused with TMS-0140. */
export function NewCarrierServicePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [carrierId, setCarrierId] = useState(params.get("carrierId") ?? "");
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [mode, setMode] = useState<(typeof LOAD_MODES)[number]>("PARCEL");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([carrierId, code, name, mode]) && !saved);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateService({ carrierId, code, name });
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createCarrierService({ carrierId: carrierId.trim(), code: code.trim().toUpperCase(), name: name.trim(), mode }, key.current);
      setSaved(true);
      release();
      navigate("/transport/carrier-services");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "TMS-0140") setErrors({ code: "This carrier already has a service with this code." });
      else if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code !== "COM-0001" && !problem.code.startsWith("HTTP-5")) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New carrier service" description="Name a service a carrier sells so rate cards and rate shopping can use it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New carrier service">
          <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
          <FormSection>
            <LookupFormField id="service-carrierId" label="Carrier" entity="vendor" hint="The carrier party." value={carrierId} error={errors.carrierId} onChange={(id) => { setCarrierId(id); key.current = newKey(); }} />
            <FormField id="service-code" label="Code" hint="Capital letters, digits, _ or -." error={errors.code}>
              {(control) => <Input {...control} maxLength={20} className={styles.mono} value={code} invalid={Boolean(errors.code)} onChange={(e) => { setCode(e.target.value.toUpperCase()); key.current = newKey(); }} />}
            </FormField>
            <FormField id="service-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} maxLength={120} value={name} invalid={Boolean(errors.name)} onChange={(e) => { setName(e.target.value); key.current = newKey(); }} />}
            </FormField>
            <FormField id="service-mode" label="Mode">
              {(control) => (
                <Select {...control} value={mode} onChange={(e) => { setMode(e.target.value as typeof mode); key.current = newKey(); }}>
                  {LOAD_MODES.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the service" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save service
            </Button>
            <Link className={buttonClassName("secondary")} to="/transport/carrier-services">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
