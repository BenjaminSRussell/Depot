import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormErrorSummary,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type BadgeTone,
  type DataTableColumn,
  type FormFieldTarget,
} from "../../shell/ui";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import {
  cancelTransportOrder,
  createRfidRule,
  describeProblem,
  getTransportOrder,
  listRfidReads,
  listRfidRules,
  listTransportOrders,
  newKey,
  ORDER_STATUSES,
  reassignWork,
  RFID_ACTIONS,
  updateRfidRule,
  type OrderStatus,
  type Problem,
  type RfidAction,
  type RfidOutcome,
  type RfidRead,
  type RfidRule,
  type RfidRuleInput,
  type TransportOrder,
  type Work,
} from "./api/client";
import styles from "./pages.module.css";
import { Detail, Named, sentence, UUID, when, WarehouseField } from "./shared";
import { useLoad } from "./useLoad";

const PAGE = 100;

const ACTION_LABELS: Record<RfidAction, string> = {
  RECEIVE_PALLET: "Receive the pallet at the dock",
  CONFIRM_PUTAWAY: "Confirm the put-away at the location",
};

const OUTCOME_TONES: Record<RfidOutcome, BadgeTone> = { DONE: "success", SKIPPED: "neutral", REFUSED: "danger" };

const ORDER_TONES: Record<OrderStatus, BadgeTone> = {
  PENDING: "neutral",
  SENT: "info",
  ARRIVED: "success",
  REFUSED: "danger",
  FAULT: "danger",
  DIVERTED: "warning",
  CANCELLED: "neutral",
};

/** The orders a person can still cancel: everything the PLC has not finished. */
const CANCELLABLE: ReadonlySet<OrderStatus> = new Set<OrderStatus>(["PENDING", "SENT", "REFUSED", "FAULT", "DIVERTED"]);
/** Orders the PLC may still hold: cancelling needs the move cleared on the PLC first. */
const WITH_PLC: ReadonlySet<OrderStatus> = new Set<OrderStatus>(["PENDING", "SENT"]);

/** A station or reader identity ("automation:PLC", "rfid:dock-1") in words. */
export function identityText(identity: string): string {
  if (identity.startsWith("automation:")) return `Station ${identity.slice("automation:".length)}`;
  if (identity.startsWith("rfid:")) return `Reader ${identity.slice("rfid:".length)}`;
  return identity;
}

/* ---------- RFID rules ---------- */

const ruleColumns: DataTableColumn<RfidRule>[] = [
  { key: "readerId", header: "Reader", mono: true },
  { key: "antenna", header: "Antenna", render: (r) => (r.antenna === undefined ? "Every antenna" : r.antenna) },
  { key: "action", header: "What a read does", render: (r) => ACTION_LABELS[r.action] ?? sentence(r.action) },
  { key: "warehouseId", header: "Warehouse", render: (r) => <Named entity="warehouse" id={r.warehouseId} /> },
  { key: "locationCode", header: "Location", mono: true, render: (r) => r.locationCode || "—" },
  { key: "postedBy", header: "Reads posted as", mono: true },
];

/** What a read at each reader does (FR-32.9): receive an ASN pallet at a dock portal, or confirm a put-away at a location. */
export function RfidRulesPage() {
  const navigate = useNavigate();
  const [reader, setReader] = useState("");
  const state = useLoad((signal) => listRfidRules(reader.trim() || undefined, signal), reader.trim());
  return (
    <div className={styles.stack}>
      <PageHeader
        title="RFID rules"
        description="Each rule says what a tag read at a reader does. A read runs a rule only when it is posted by the identity the rule names."
        actions={
          <Link className={buttonClassName("primary")} to="/warehouse/rfid-rules/new">
            New RFID rule
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rule-reader" label="Reader" optional>
          {(c) => <Input {...c} value={reader} onChange={(e) => setReader(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the RFID rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="RFID rules"
          columns={ruleColumns}
          rows={state.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No RFID rules", description: "Add a rule so a read at a dock portal or a location does something." }}
          onRowOpen={(r) => navigate(`/warehouse/rfid-rules/${r.id}`)}
        />
      )}
    </div>
  );
}

interface RuleValues {
  readerId: string;
  antenna: string;
  action: RfidAction;
  warehouseId: string;
  locationCode: string;
  postedBy: string;
}

type RuleErrors = Partial<Record<keyof RuleValues, string>>;

const RULE_FIELDS: Record<string, FormFieldTarget> = {
  readerId: { id: "rule-readerId", label: "Reader" },
  antenna: { id: "rule-antenna", label: "Antenna" },
  action: { id: "rule-action", label: "What a read does" },
  warehouseId: { id: "wh-rule-warehouse", label: "Warehouse" },
  locationCode: { id: "rule-locationCode", label: "Location" },
  postedBy: { id: "rule-postedBy", label: "Reads posted as" },
};

const EMPTY_RULE: RuleValues = { readerId: "", antenna: "", action: "RECEIVE_PALLET", warehouseId: "", locationCode: "", postedBy: "" };

/** Checks that mirror the service, and the request the form sends. */
export function validateRule(values: RuleValues): { errors: RuleErrors; input?: RfidRuleInput } {
  const errors: RuleErrors = {};
  const reader = values.readerId.trim();
  if (reader === "" || reader.length > 64) errors.readerId = "Enter the reader, up to 64 characters.";
  const antenna = values.antenna.trim();
  if (antenna !== "" && !(/^\d{1,3}$/.test(antenna) && Number(antenna) <= 255)) errors.antenna = "Enter an antenna from 0 to 255, or leave it empty for every antenna.";
  if (!UUID.test(values.warehouseId.trim())) errors.warehouseId = "Choose the warehouse the rule acts in.";
  const location = values.locationCode.trim();
  if (location.length > 60) errors.locationCode = "Use at most 60 characters.";
  else if (values.action === "CONFIRM_PUTAWAY" && location === "") errors.locationCode = "Enter the location the reader watches.";
  const poster = values.postedBy.trim();
  if (poster === "" || poster.length > 200) errors.postedBy = "Enter the identity the reader's gateway posts its reads as.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      readerId: reader,
      ...(antenna === "" ? {} : { antenna: Number(antenna) }),
      action: values.action,
      warehouseId: values.warehouseId.trim().toLowerCase(),
      ...(location === "" ? {} : { locationCode: location }),
      postedBy: poster,
    },
  };
}

function RuleForm({ rule }: { rule?: RfidRule }) {
  const navigate = useNavigate();
  const [values, setValues] = useState<RuleValues>(
    rule
      ? { readerId: rule.readerId, antenna: rule.antenna === undefined ? "" : String(rule.antenna), action: rule.action, warehouseId: rule.warehouseId, locationCode: rule.locationCode ?? "", postedBy: rule.postedBy }
      : EMPTY_RULE,
  );
  const [errors, setErrors] = useState<RuleErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));
  const editing = rule !== undefined;

  const update = (patch: Partial<RuleValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      if (rule) await updateRfidRule(rule.id, input);
      else await createRfidRule(input);
      release();
      navigate("/warehouse/rfid-rules");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: RuleErrors = {};
      for (const name of Object.keys(RULE_FIELDS) as (keyof RuleValues)[]) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "readerId" | "antenna" | "locationCode" | "postedBy", label: string, hint: string, optional = false, extra: Record<string, unknown> = {}) => (
    <FormField id={`rule-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(c) => <Input {...c} {...extra} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={editing ? `Rule for reader ${rule.readerId}` : "New RFID rule"}
        description="A reader has one rule for each antenna and action. Reads already acted on keep what was done."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "RFID rule" : "New RFID rule"}>
          <FormErrorSummary items={summaryItems(errors, RULE_FIELDS)} attempt={attempt} />
          {text("readerId", "Reader", "The reader's name as its gateway posts it, for example dock-1.", false, { maxLength: 64 })}
          {text("antenna", "Antenna", "Leave it empty for every antenna of the reader.", true, { inputMode: "numeric" })}
          <FormField id="rule-action" label="What a read does" hint="A dock portal receives the pallet its tag names; a reader at a location confirms that pallet's put-away." error={errors.action}>
            {(c) => (
              <Select {...c} value={values.action} onChange={(e) => update({ action: e.target.value as RfidAction })}>
                {RFID_ACTIONS.map((a) => (
                  <option key={a} value={a}>
                    {ACTION_LABELS[a]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <WarehouseField id="wh-rule-warehouse" value={values.warehouseId} onChange={(id) => update({ warehouseId: id })} error={errors.warehouseId} />
          {text("locationCode", "Location", values.action === "CONFIRM_PUTAWAY" ? "The location of the warehouse the reader watches." : "Only a put-away rule needs a location.", values.action !== "CONFIRM_PUTAWAY", { maxLength: 60 })}
          {text("postedBy", "Reads posted as", "The identity the reader's gateway posts its reads as. A read posted by anyone else is stored but runs no rule.", false, { maxLength: 200 })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "WMS-RFID-RULE-EXISTS" ? "This reader already has that rule" : "Could not save the rule"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {editing ? "Save rule" : "Create rule"}
            </Button>
            <Link className={buttonClassName("ghost")} to="/warehouse/rfid-rules">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}

export function NewRfidRulePage() {
  return <RuleForm />;
}

export function RfidRuleDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad(async (signal) => (await listRfidRules(undefined, signal)).items.find((r) => r.id === id) ?? null, id);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="RFID rule" />
        <Alert tone="danger" title="Could not load the rule" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="RFID rule" />
        <span className="visually-hidden">Loading the rule…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="RFID rule" />
        <Alert tone="warning" title="This rule no longer exists">
          <Link to="/warehouse/rfid-rules">Back to the RFID rules</Link>
        </Alert>
      </div>
    );
  }
  return <RuleForm key={state.data.id} rule={state.data} />;
}

/* ---------- RFID reads ---------- */

const readColumns: DataTableColumn<RfidRead>[] = [
  { key: "readAt", header: "Read", mono: true, render: (r) => when(r.readAt) },
  { key: "readerId", header: "Reader", mono: true, render: (r) => `${r.readerId}, antenna ${r.antenna}` },
  { key: "key", header: "Tag", mono: true, render: (r) => `${r.scheme} ${r.key}${r.serial ? ` / ${r.serial}` : ""}` },
  { key: "rssi", header: "Signal", align: "end", render: (r) => (r.rssi === undefined ? "—" : `${r.rssi} dBm`) },
  {
    key: "actionOutcome",
    header: "What it did",
    render: (r) =>
      r.action && r.actionOutcome ? (
        <>
          <Badge tone={OUTCOME_TONES[r.actionOutcome] ?? "neutral"}>{sentence(r.actionOutcome)}</Badge> {ACTION_LABELS[r.action] ?? sentence(r.action)}
          {r.actionDetail ? <div className={styles.note}>{r.actionDetail}</div> : null}
        </>
      ) : (
        "No rule ran"
      ),
  },
];

/** Tag reads the warehouse stored, newest first, with what each rule did with them. */
export function RfidReadsPage() {
  const [reader, setReader] = useState("");
  const [key, setKey] = useState("");
  const filter = { readerId: reader.trim(), key: key.trim(), limit: PAGE };
  const state = useLoad((signal) => listRfidReads(filter, signal), `${filter.readerId}|${filter.key}`);
  return (
    <div className={styles.stack}>
      <PageHeader title="RFID reads" description="Every tag read the warehouse stored, newest first, and what the reader's rule did with it." />
      <div className={styles.toolbar}>
        <FormField id="read-reader" label="Reader" optional>
          {(c) => <Input {...c} value={reader} onChange={(e) => setReader(e.target.value)} />}
        </FormField>
        <FormField id="read-key" label="Pallet number or item number" hint="The SSCC or GTIN the tag identifies." optional>
          {(c) => <Input {...c} className={styles.mono} value={key} onChange={(e) => setKey(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the reads" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable label="RFID reads" columns={readColumns} rows={state.data?.items ?? []} getRowKey={(r) => r.id} loading={state.loading} empty={{ title: "No reads", description: "Reads appear here once a reader's gateway posts them." }} />
      )}
      {(state.data?.items.length ?? 0) >= PAGE ? <p className={styles.note}>Showing the newest {PAGE} reads; filter by reader or tag to narrow the list.</p> : null}
    </div>
  );
}

/* ---------- Automation transport orders ---------- */

/** The moves the warehouse asked a PLC station to make, oldest first (the order the PLC works them). */
export function TransportOrdersPage() {
  const navigate = useNavigate();
  const [station, setStation] = useState("");
  const [status, setStatus] = useState("");
  const state = useLoad((signal) => listTransportOrders({ station: station.trim(), status, limit: PAGE }, signal), `${station.trim()}|${status}`);
  const columns: DataTableColumn<TransportOrder>[] = [
    { key: "loadUnit", header: "Pallet", mono: true },
    { key: "station", header: "Station", mono: true },
    { key: "move", header: "Move", render: (o) => `${o.fromLocation} to ${o.toLocation}` },
    { key: "status", header: "Status", render: (o) => <Badge tone={ORDER_TONES[o.status] ?? "neutral"}>{sentence(o.status)}</Badge> },
    { key: "createdAt", header: "Asked", mono: true, render: (o) => when(o.createdAt) },
    { key: "reason", header: "Why", render: (o) => o.reason || o.confirmError || "—" },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader title="Automation orders" description="Moves the warehouse asked an automated station to make. Open an order to see its work or to cancel it." />
      <div className={styles.toolbar}>
        <FormField id="order-station" label="Station" optional>
          {(c) => <Input {...c} value={station} onChange={(e) => setStation(e.target.value)} />}
        </FormField>
        <FormField id="order-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {ORDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Automation orders"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(o) => o.id}
          loading={state.loading}
          empty={{ title: "No automation orders", description: "Orders appear here when transport work is given to a station." }}
          onRowOpen={(o) => navigate(`/warehouse/automation-orders/${o.id}`)}
        />
      )}
      {(state.data?.items.length ?? 0) >= PAGE ? <p className={styles.note}>Showing the oldest {PAGE} matching orders; filter by status to narrow the list.</p> : null}
    </div>
  );
}

const CANCEL_TITLES: Record<string, string> = {
  "WMS-AUTO-ORDER-STATE": "The order could not be cancelled",
  "WMS-AUTO-ORDER-NOT-FOUND": "The order no longer exists",
};

/** One automation order, with the cancel form: an order the PLC may hold is cancelled only once the move is cleared there. */
export function TransportOrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getTransportOrder(id, signal), id);
  const [reason, setReason] = useState("");
  const [cleared, setCleared] = useState(false);
  const [errors, setErrors] = useState<{ reason?: string; plcCleared?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [current, setCurrent] = useState<TransportOrder | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const order = current ?? state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Automation order" />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!order) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Automation order" />
        <span className="visually-hidden">Loading the order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = order;
  const withPlc = WITH_PLC.has(loaded.status);

  async function cancel(event: FormEvent) {
    event.preventDefault();
    const found: { reason?: string; plcCleared?: string } = {};
    const why = reason.trim();
    if (why === "" || why.length > 200) found.reason = "Enter why the order is cancelled, up to 200 characters.";
    if (withPlc && !cleared) found.plcCleared = "Confirm that the move is cleared on the PLC before cancelling.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setCurrent(await cancelTransportOrder(loaded.id, { reason: why, plcCleared: cleared }));
      setReason("");
      setCleared(false);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`Order for pallet ${loaded.loadUnit}`} meta={<Badge tone={ORDER_TONES[loaded.status] ?? "neutral"}>{sentence(loaded.status)}</Badge>} />
      <Card>
        <dl className={styles.details}>
          <Detail term="Station" mono>
            {loaded.station}
          </Detail>
          <Detail term="Pallet" mono>
            {loaded.loadUnit}
          </Detail>
          <Detail term="Move">{`${loaded.fromLocation} to ${loaded.toLocation}`}</Detail>
          <Detail term="Priority">{loaded.priority}</Detail>
          <Detail term="Work">
            <Link to={`/warehouse/work/${loaded.workId}`}>{`Open the work, line ${loaded.lineNo}`}</Link>
          </Detail>
          <Detail term="Asked">{when(loaded.createdAt)}</Detail>
          <Detail term="Sent to the PLC">{when(loaded.sentAt)}</Detail>
          <Detail term="Finished">{when(loaded.doneAt)}</Detail>
          {loaded.reason ? <Detail term="Why it ended">{loaded.reason}</Detail> : null}
          {loaded.confirmError ? <Detail term="Why the arrival was not confirmed">{loaded.confirmError}</Detail> : null}
        </dl>
      </Card>
      {CANCELLABLE.has(loaded.status) ? (
        <Card title="Cancel the order">
          <form className={styles.form} onSubmit={cancel} noValidate aria-label="Cancel the order">
            <FormErrorSummary
              items={summaryItems(errors as Record<string, string>, { reason: { id: "cancel-order-reason", label: "Reason" }, plcCleared: { id: "cancel-order-cleared", label: "Cleared on the PLC" } })}
              attempt={attempt}
            />
            {withPlc ? (
              <p className={styles.note}>
                The PLC may already hold this move: the connector sends it before it reports the order sent. Clear the move on the PLC first, then cancel the order here so the work goes back to people.
              </p>
            ) : (
              <p className={styles.note}>The work goes back to people when no other order of it is with the PLC.</p>
            )}
            <FormField id="cancel-order-reason" label="Reason" error={errors.reason}>
              {(c) => <Input {...c} maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
            </FormField>
            {withPlc ? (
              <FormField id="cancel-order-cleared" label="Cleared on the PLC" error={errors.plcCleared}>
                {(c) => (
                  <label className={styles.check}>
                    <Input {...c} type="checkbox" checked={cleared} onChange={(e) => setCleared(e.target.checked)} /> I cleared this move on the PLC
                  </label>
                )}
              </FormField>
            ) : null}
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title={CANCEL_TITLES[failure.code] ?? "Could not cancel the order"} live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={busy}>
                Cancel order
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}

/* ---------- Hand a station's work to a person ---------- */

/** True for a work an automated station holds, which a supervisor can hand to a person after a fault. */
export function heldByStation(work: Pick<Work, "status" | "assignedUser">): boolean {
  return (work.status === "ASSIGNED" || work.status === "IN_PROGRESS") && (work.assignedUser ?? "").startsWith("automation:");
}

/**
 * After a PLC fault or divert a work the station already confirmed a line of stays IN_PROGRESS with the station. The
 * supervisor gives it to a person here; its confirmed lines stay as they are and the person confirms the rest. The service
 * refuses while an order of the work is still with the PLC: cancel that order first.
 */
export function ReassignCard({ work, onDone }: { work: Work; onDone(work: Work): void }) {
  const [toUser, setToUser] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ toUser?: string; reason?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: { toUser?: string; reason?: string } = {};
    if (toUser.trim() === "") found.toUser = "Choose the person who takes the work.";
    const why = reason.trim();
    if (why === "" || why.length > 150) found.reason = "Enter why the work changes hands, up to 150 characters.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await reassignWork(work, { toUser: toUser.trim(), reason: why }, key.current);
      key.current = newKey();
      onDone(out);
    } catch (error) {
      setFailure(describeProblem(error));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Hand the work to a person">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Hand the work to a person">
        <FormErrorSummary items={summaryItems(errors as Record<string, string>, { toUser: { id: "reassign-user", label: "Person" }, reason: { id: "reassign-reason", label: "Reason" } })} attempt={attempt} />
        <p className={styles.note}>
          {`${identityText(work.assignedUser ?? "")} holds this work. Lines it already confirmed stay confirmed; the person you choose confirms the rest. If an order of the work is still with the PLC, cancel it first.`}
        </p>
        <FormField id="reassign-user" label="Person" error={errors.toUser}>
          {(c) => <EntityLookup {...c} entity="user" value={toUser} onChange={(e) => setToUser(e.target.value)} invalid={Boolean(errors.toUser)} />}
        </FormField>
        <FormField id="reassign-reason" label="Reason" hint="For example the PLC fault at the infeed." error={errors.reason}>
          {(c) => <Input {...c} maxLength={150} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure.code === "WMS-AUTO-ORDER-STATE" ? "An order of this work is still with the PLC" : "Could not hand over the work"} live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Hand to this person
          </Button>
        </div>
      </form>
    </Card>
  );
}
