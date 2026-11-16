// Shop floor service (/api/v1/shopfloor) for the demo: machines, the andon board and OEE.
// Resources and sites carry the real ids of company.ts; the screens show their names.
import type { Andon, AndonSettings, AndonSiteSettings, AndonThresholds, ControlRecipeDetail, Downtime, EdgeCertificate, EdgeConfigVersion, EdgeNode, Job, Machine, OeeBucket, OeeTile, PICategory, PIExecution, PIExecutionDetail, PIExecutionStep, PISheet, PIStepSpec, PIStepTemplate, RecipeDocument } from "../../../modules/shopfloor/api/client";
import { reply, route } from "../../backend";
import { DC, PLANT, resourceByCode } from "../../company";
import { at, table, uid } from "../../store";
import { ME, byUsername } from "../finance/people";
import { orders } from "./production";

const BASE = "/api/v1/shopfloor";
const KIND_ANDON = 230;
const KIND_DOWNTIME = 231;
const KIND_MACHINE = 232;

const MACHINES = [
  { code: "M-101", resource: "MCH-01", cycle: 90, seed: 3 },
  { code: "M-102", resource: "BAL-01", cycle: 60, seed: 7 },
  { code: "M-103", resource: "ASM-01", cycle: 120, seed: 11 },
  { code: "M-104", resource: "TST-01", cycle: 100, seed: 5 },
  { code: "M-105", resource: "PNT-01", cycle: 110, seed: 13 },
].map((m) => ({ ...m, resource: resourceByCode(m.resource).id, resourceCode: m.resource }));

const machines: Machine[] = MACHINES.map((m, i) => ({
  id: uid(KIND_MACHINE, i + 1),
  machineId: m.code,
  resourceId: m.resource,
  siteId: PLANT.id,
  idealCycleSeconds: String(m.cycle),
  idleThresholdSeconds: 300,
  lastSeq: 18000 + i * 731,
  createdAt: at(-200),
}));

const downtimes: Downtime[] = [
  { id: uid(KIND_DOWNTIME, 1), machineId: "M-102", resourceId: resourceByCode("BAL-01").id, startedAt: at(-0.06), endedAt: null, reasonCode: null, category: "unplanned", cause: "down", andonRaised: true },
  { id: uid(KIND_DOWNTIME, 2), machineId: "M-105", resourceId: resourceByCode("PNT-01").id, startedAt: at(-0.04), endedAt: null, reasonCode: null, category: "unplanned", cause: "idle", andonRaised: true },
];

const andon = table<Andon>([
  { id: uid(KIND_ANDON, 1), resourceId: resourceByCode("BAL-01").id, siteId: PLANT.id, kind: "downtime", severity: "error", downtimeId: downtimes[0].id, jobId: null, detail: "Balancer drive fault E-214 while balancing MO-24618", startedAt: at(-0.06) },
  { id: uid(KIND_ANDON, 2), resourceId: resourceByCode("MCH-01").id, siteId: PLANT.id, kind: "scrap-rate", severity: "warning", downtimeId: null, jobId: null, detail: "Scrap 6.8 % over the last hour on volute housings, machined, 4 in (limit 5 %)", startedAt: at(-0.15) },
  { id: uid(KIND_ANDON, 3), resourceId: resourceByCode("TST-01").id, siteId: PLANT.id, kind: "quality-fail", severity: "error", downtimeId: null, jobId: "JOB-24611-20", detail: "Hydrostatic test failed on MO-24611: seal weep at 450 psi, unit 24611-014", startedAt: at(-0.3) },
  { id: uid(KIND_ANDON, 4), resourceId: resourceByCode("PNT-01").id, siteId: PLANT.id, kind: "downtime", severity: "warning", downtimeId: downtimes[1].id, jobId: null, detail: "Booth idle for 58 min: waiting for 2 in pump housings to coat", startedAt: at(-0.04) },
]);

route("GET", `${BASE}/machines`, () => ({ items: machines }));
route("GET", `${BASE}/downtimes`, (req) => {
  const open = req.query.get("open");
  let rows = downtimes;
  if (open === "true") rows = rows.filter((d) => d.endedAt === null);
  if (open === "false") rows = rows.filter((d) => d.endedAt !== null || d.reasonCode === null);
  return { items: rows };
});
route("GET", `${BASE}/andon`, () => ({ items: andon.all().sort((a, b) => b.startedAt.localeCompare(a.startedAt)) }));
route("POST", `${BASE}/andon/{id}:clear`, (req) => {
  const row = andon.get(req.params.id);
  if (!row) return reply.problem(404, "SF-0162", "The alert is unknown or already cleared.");
  andon.remove(row.id);
  return reply.ok(row);
});

/** A repeatable pseudo-random number in [0, 1) so the OEE chart is stable between reloads. */
function noise(a: number, b: number): number {
  const x = Math.sin(a * 127.1 + b * 311.7) * 43758.5453;
  return x - Math.floor(x);
}

const HOUR = 3_600_000;
const BUCKET_MINUTES = { "5m": 5, "15m": 15, "60m": 60 } as const;

/**
 * One machine's bucket. The plant runs three shifts (06:00 to 14:00, 14:00 to 22:00, 22:00 to 06:00 local), so every
 * bucket of the day has planned time; the first quarter hour of a shift is lost to handover, the balancer stops for
 * a drive fault when `faultFrom` falls in the bucket, and the others lose a few short stops.
 */
function bucketOf(m: (typeof MACHINES)[number], start: number, size: number, dayStart: number, faultFrom: number): OeeBucket {
  const slot = Math.floor(start / (size * 60_000));
  const n = noise(m.seed, slot);
  const hourOfDay = (((start - dayStart) / HOUR) % 24 + 24) % 24;
  const sinceShift = ((hourOfDay - 6 + 24) % 8) * 60;
  const handover = sinceShift < 15 ? Math.min(size, 15 - sinceShift) * 0.8 : 0;
  const faulted = m.resourceCode === "BAL-01" && start + size * 60_000 > faultFrom && start < faultFrom + HOUR;
  const night = hourOfDay >= 22 || hourOfDay < 6;
  const small = n < (night ? 0.18 : 0.12) ? size * (0.15 + n * 1.4) : 0;
  const down = Math.min(size, faulted ? size : handover + small);
  const run = size - down;
  const availability = run / size;
  const performance = Math.min(1, (night ? 0.82 : 0.87) + noise(slot, m.seed) * 0.12);
  const quality = m.resourceCode === "MCH-01" ? 0.93 + noise(m.seed, slot + 9) * 0.05 : 0.975 + noise(m.seed, slot + 4) * 0.025;
  const total = Math.floor(((run * 60) / m.cycle) * performance);
  const good = Math.round(total * quality);
  const q = total === 0 ? 1 : good / total;
  return {
    resourceId: m.resource,
    bucketStart: new Date(start).toISOString(),
    plannedMinutes: size.toFixed(3),
    runMinutes: run.toFixed(3),
    downMinutes: down.toFixed(3),
    totalCount: total,
    goodCount: good,
    availability: availability.toFixed(4),
    performance: performance.toFixed(4),
    quality: q.toFixed(4),
    oee: (availability * performance * q).toFixed(4),
    overRate: false,
    sourceEvents: 6 + Math.floor(n * 20),
  };
}

/** When the balancer's drive fault stopped it: 90 minutes ago for today, mid-morning for another day. */
const faultStart = (dayStart: number) => (Date.now() >= dayStart && Date.now() < dayStart + 24 * HOUR ? Date.now() - 1.5 * HOUR : dayStart + 8.5 * HOUR);

route("GET", `${BASE}/oee/stream`, (req) => {
  const m = MACHINES.find((x) => x.resource === req.query.get("resourceId"));
  const size = BUCKET_MINUTES[(req.query.get("bucket") ?? "15m") as keyof typeof BUCKET_MINUTES] ?? 15;
  if (!m) return reply.events([]);
  const now = Date.now();
  const start = Math.floor(now / (size * 60_000)) * size * 60_000;
  // The count grows through the bucket, so the tile moves between pushes.
  const share = Math.max(0.2, (now - start) / 60_000 / size);
  const dayStart = Date.parse(`${new Date(now).toISOString().slice(0, 10)}T00:00:00Z`);
  const full = bucketOf(m, start, size, dayStart, faultStart(dayStart));
  const wobble = Math.floor(noise(m.seed, Math.floor(now / 4000)) * 3);
  const bucket: OeeBucket = {
    ...full,
    plannedMinutes: (size * share).toFixed(3),
    runMinutes: (Number(full.runMinutes) * share).toFixed(3),
    downMinutes: (Number(full.downMinutes) * share).toFixed(3),
    totalCount: Math.floor(full.totalCount * share) + wobble,
    goodCount: Math.floor(full.goodCount * share) + wobble,
  };
  const tile: OeeTile = { resourceId: m.resource, bucketStart: new Date(start).toISOString(), bucketEnd: new Date(start + size * 60_000).toISOString(), computedAt: new Date(now).toISOString(), bucket };
  return reply.events([{ event: "oee", data: tile }], 4000);
});

route("GET", `${BASE}/oee`, (req) => {
  const ids = (req.query.get("resourceIds") ?? "").split(",").filter(Boolean);
  const from = Date.parse(req.query.get("from") ?? "") || Date.parse(`${new Date().toISOString().slice(0, 10)}T00:00:00Z`);
  const to = Date.parse(req.query.get("to") ?? "") || from + 24 * HOUR;
  const size = BUCKET_MINUTES[(req.query.get("bucket") ?? "15m") as keyof typeof BUCKET_MINUTES] ?? 15;
  const items: OeeBucket[] = [];
  const fault = faultStart(from);
  for (const m of MACHINES.filter((x) => ids.includes(x.resource))) {
    for (let t = from; t < to && t < from + 36 * HOUR; t += size * 60_000) items.push(bucketOf(m, t, size, from, fault));
  }
  return { items };
});

// The dispatch list: what the schedule released to each work center, with the checks the operator records.
const JOB_SPECS = [
  { n: 1, order: "MO-24611", op: 20, res: "TST-01", status: "started" as const, plan: 24, good: 10, scrap: 1, start: -0.4 },
  { n: 2, order: "MO-24618", op: 20, res: "BAL-01", status: "started" as const, plan: 60, good: 24, scrap: 2, start: -1.2 },
  { n: 3, order: "MO-24627", op: 10, res: "ASM-01", status: "released" as const, plan: 30, good: 0, scrap: 0, start: 0.1 },
  { n: 4, order: "MO-24605", op: 10, res: "ASM-01", status: "released" as const, plan: 4, good: 0, scrap: 0, start: 0.8 },
  { n: 5, order: "MO-24624", op: 10, res: "MCH-01", status: "released" as const, plan: 20, good: 0, scrap: 0, start: 0.2 },
  { n: 6, order: "MO-24608", op: 40, res: "PCK-01", status: "finished" as const, plan: 40, good: 40, scrap: 0, start: -1.9 },
];
// Job ids read as order and operation (JOB-24611-20), since the supervisor screens show them as they are.
export const jobs = table<Job>(
  JOB_SPECS.map((j) => ({
    id: `JOB-${j.order.slice(3)}-${j.op}`,
    orderRef: j.order,
    operationRef: `${j.op}`,
    resourceId: resourceByCode(j.res).id,
    status: j.status,
    plannedQuantity: j.plan.toFixed(4),
    goodQuantity: j.good.toFixed(4),
    scrapQuantity: j.scrap.toFixed(4),
    sequenceStart: at(j.start),
    checks:
      j.res === "TST-01"
        ? [
            { code: "HYDRO", name: "Hydrostatic pressure hold, psi", kind: "numeric" as const, min: "450", max: "520", mandatory: true },
            { code: "LEAK", name: "No visible leak at the seal", kind: "pass-fail" as const, mandatory: true },
          ]
        : [{ code: "VISUAL", name: "Visual check", kind: "pass-fail" as const, mandatory: false }],
    results: j.res === "TST-01" ? [{ code: "HYDRO", value: "452", pass: true }, { code: "LEAK", value: "fail", pass: false }] : [],
    finishedAt: j.status === "finished" ? at(j.start + 0.3) : null,
    version: 1,
    updatedAt: at(j.start + 0.1),
  })),
);
route("GET", `${BASE}/jobs`, (req) => {
  const resource = req.query.get("resourceId");
  const status = req.query.get("status");
  let rows = jobs.all();
  if (resource) rows = rows.filter((j) => j.resourceId === resource);
  rows = status ? rows.filter((j) => j.status === status) : rows.filter((j) => j.status !== "finished");
  return { items: rows.sort((a, b) => a.sequenceStart.localeCompare(b.sequenceStart)).map((j) => ({ ...j, results: [] })) };
});
route("GET", `${BASE}/jobs/{id}/reopenings`, () => ({ items: [] }));
route("GET", `${BASE}/jobs/{id}`, (req) => jobs.get(req.params.id) ?? reply.problem(404, "SF-0404", "Job not found."));


// Control recipes (FR-25.15): the recipe of a released order sent to a kiosk or to a process control system.
const orderId = (number: string): string => orders.all().find((o) => o.orderNumber === number)?.id ?? uid(234, 99);
const recipeDoc = (setpoint?: { node: string; value: number }): RecipeDocument => ({
  procedure: {
    name: "Impeller balancing",
    unitProcedures: [
      {
        name: "Balancer",
        operations: [
          {
            name: "Balance",
            phases: [
              {
                name: "Spin-up",
                instructions: [
                  { seq: 1, category: "instruction-text" as const, text: "Mount the impeller on the arbor and check the runout." },
                  ...(setpoint ? [{ seq: 2, category: "process-parameter-setpoint" as const, node: setpoint.node, value: setpoint.value }] : []),
                ],
              },
            ],
          },
        ],
      },
    ],
  },
});
/** The pump assembly sheet a kiosk runs: instruction, setpoint, a measured value with limits, an inspection and a signature. */
function assemblySheet(): RecipeDocument {
  return {
    procedure: {
      name: "Pump assembly",
      unitProcedures: [
        {
          name: "Assembly cell",
          operations: [
            {
              name: "Assemble",
              phases: [
                {
                  name: "Fit the impeller",
                  instructions: [
                    { seq: 1, category: "instruction-text", text: "Check that the casing is clean and the wear ring is seated." },
                    { seq: 2, category: "process-parameter-setpoint", text: "Set the torque wrench for the impeller nut.", value: 45, characteristic: { name: "Nut torque", type: "numeric", unit: "N m", min: 40, max: 50 } },
                    { seq: 3, category: "process-data-request", text: "Measure the impeller end float.", characteristic: { name: "End float", type: "numeric", unit: "mm", min: 0.2, max: 0.5 } },
                  ],
                },
                {
                  name: "Test and sign",
                  instructions: [
                    { seq: 1, category: "inspection-characteristic", text: "Spin the shaft by hand. Does it turn freely?", characteristic: { name: "Turns freely", type: "boolean" } },
                    { seq: 2, category: "signature-request", text: "Sign to confirm the assembly followed this sheet." },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  };
}
const recipeBase = { topic: null, version: 3, createdBy: "Maya Okafor", createdAt: at(-0.5), updatedAt: at(-0.2) };
const recipes = table<ControlRecipeDetail>([
  {
    ...recipeBase,
    id: uid(234, 1),
    orderId: orderId("MO-24618"),
    resourceId: resourceByCode("BAL-01").id,
    destination: "pcs-opcua",
    edgeId: uid(235, 1),
    status: "in-execution",
    recipe: recipeDoc({ node: "ns=2;s=Balancer1.SpeedSP", value: 1800 }),
    sendCount: 1,
    lastSentAt: at(-0.5),
    lastError: null,
    history: [
      { id: uid(236, 1), fromStatus: null, toStatus: "sent", actor: "Maya Okafor", note: null, forced: false, at: at(-0.5) },
      { id: uid(236, 2), fromStatus: "sent", toStatus: "received", actor: "edge:plant-gateway", note: null, forced: false, at: at(-0.49) },
      { id: uid(236, 3), fromStatus: "received", toStatus: "in-execution", actor: "edge:plant-gateway", note: null, forced: false, at: at(-0.48) },
    ],
    writes: [
      { id: uid(237, 1), phasePath: "Impeller balancing/Balancer/Balance/Spin-up", node: "ns=2;s=Balancer1.SpeedSP", value: "1800", readback: "1800", written: true, deviation: false, reason: null, at: at(-0.48) },
    ],
  },
  {
    ...recipeBase,
    id: uid(234, 2),
    orderId: orderId("MO-24627"),
    resourceId: resourceByCode("ASM-01").id,
    destination: "kiosk",
    edgeId: null,
    status: "sent",
    recipe: assemblySheet(),
    sendCount: 1,
    lastSentAt: at(-0.1),
    lastError: null,
    history: [{ id: uid(236, 4), fromStatus: null, toStatus: "sent", actor: "Maya Okafor", note: null, forced: false, at: at(-0.1) }],
    writes: [],
  },
  {
    ...recipeBase,
    id: uid(234, 3),
    orderId: orderId("MO-24611"),
    resourceId: resourceByCode("TST-01").id,
    destination: "pcs-opcua",
    edgeId: uid(235, 1),
    status: "error",
    recipe: recipeDoc({ node: "ns=2;s=Test1.PressureSP", value: 480 }),
    sendCount: 2,
    lastSentAt: at(-0.3),
    lastError: "1 of 1 setpoints were refused or failed; see the write audit",
    history: [
      { id: uid(236, 5), fromStatus: null, toStatus: "sent", actor: "Maya Okafor", note: null, forced: false, at: at(-0.6) },
      { id: uid(236, 6), fromStatus: "sent", toStatus: "error", actor: "edge:plant-gateway", note: "1 of 1 setpoints were refused or failed; see the write audit", forced: false, at: at(-0.55) },
      { id: uid(236, 7), fromStatus: "error", toStatus: "sent", actor: "Maya Okafor", note: "Test stand node added to the allowlist", forced: false, at: at(-0.3) },
    ],
    writes: [
      { id: uid(237, 2), phasePath: "Impeller balancing/Balancer/Balance/Spin-up", node: "ns=2;s=Test1.PressureSP", value: "480", readback: null, written: false, deviation: false, reason: "node is not on the write allowlist", at: at(-0.55) },
    ],
  },
]);
const asRow = ({ history: _history, writes: _writes, ...row }: ControlRecipeDetail) => row;
route("GET", `${BASE}/control-recipes`, (req) => {
  const status = req.query.get("status");
  const destination = req.query.get("destination");
  const rows = recipes
    .all()
    .filter((r) => (!status || r.status === status) && (!destination || r.destination === destination))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || a.id.localeCompare(b.id));
  return { items: rows.map(asRow) };
});
route("GET", `${BASE}/control-recipes/{id}`, (req) => recipes.get(req.params.id) ?? reply.problem(404, "SF-0174", "Control recipe not found."));
route("POST", `${BASE}/control-recipes/{id}:resend`, (req) => {
  const row = recipes.get(req.params.id);
  if (!row) return reply.problem(404, "SF-0174", "Control recipe not found.");
  if (row.status !== "error") return reply.problem(409, "SF-0175", "Only a control recipe in error can be sent again.");
  const body = (req.body ?? {}) as { reason?: string; force?: boolean };
  const next = recipes.update(row.id, {
    status: "sent",
    sendCount: row.sendCount + 1,
    lastError: null,
    lastSentAt: new Date().toISOString(),
    history: [...row.history, { id: crypto.randomUUID(), fromStatus: "error", toStatus: "sent", actor: "Jordan Avery", note: body.reason?.trim() || null, forced: Boolean(body.force), at: new Date().toISOString() }],
  });
  return reply.ok(next ? asRow(next) : row);
});

// Kiosk reports and the values the operator enters (FR-25.16): the demo keeps the status and accepts the messages.
route("POST", `${BASE}/control-recipes/{id}:report`, (req) => {
  const row = recipes.get(req.params.id);
  if (!row) return reply.problem(404, "SF-0174", "Control recipe not found.");
  const body = (req.body ?? {}) as { status?: ControlRecipeDetail["status"]; error?: string };
  if (!body.status) return reply.problem(400, "COM-0001", "The request is not valid.");
  const next = recipes.update(row.id, {
    status: body.status,
    lastError: body.status === "error" ? (body.error ?? null) : null,
    history: [...row.history, { id: crypto.randomUUID(), fromStatus: row.status, toStatus: body.status, actor: "Sam Ortiz", note: null, forced: false, at: new Date().toISOString() }],
  });
  return reply.ok(next ? asRow(next) : row);
});
route("POST", `${BASE}/process-messages`, (req) => {
  const body = (req.body ?? {}) as { sourceMessageId?: string; categoryCode?: string; orderId?: string; values?: unknown[] };
  return reply.created({ message: { id: crypto.randomUUID(), sourceMessageId: body.sourceMessageId, categoryCode: body.categoryCode, orderId: body.orderId, status: "delivered", values: body.values ?? [] }, duplicate: false });
});

// PI sheets (FR-25.16): the designer's versioned templates.
const sheetBase = { createdBy: "Maya Okafor", updatedBy: "Maya Okafor" };
const piSheets = table<PISheet>([
  { ...sheetBase, id: uid(388, 1), code: "PUMP-ASSY", version: 2, name: "Pump assembly", status: "released", recipe: assemblySheet(), rev: 4, createdAt: at(-20), updatedAt: at(-9), releasedBy: "Maya Okafor", releasedAt: at(-9) },
  { ...sheetBase, id: uid(388, 2), code: "PUMP-ASSY", version: 1, name: "Pump assembly", status: "obsolete", recipe: recipeDoc(), rev: 5, createdAt: at(-60), updatedAt: at(-9), releasedBy: "Maya Okafor", releasedAt: at(-50) },
  { ...sheetBase, id: uid(388, 3), code: "IMPELLER-BAL", version: 1, name: "Impeller balancing", status: "draft", recipe: recipeDoc({ node: "ns=2;s=Balancer1.SpeedSP", value: 1800 }), rev: 2, createdAt: at(-3), updatedAt: at(-1), releasedBy: null, releasedAt: null },
]);
const NO_SHEET = () => reply.problem(404, "SF-0180", "PI sheet not found.");
route("GET", `${BASE}/pi-sheets`, (req) => {
  const status = req.query.get("status");
  const code = req.query.get("code");
  return { items: piSheets.all().filter((s) => (!status || s.status === status) && (!code || s.code === code)).sort((a, b) => a.code.localeCompare(b.code) || b.version - a.version) };
});
route("GET", `${BASE}/pi-sheets/{id}`, (req) => piSheets.get(req.params.id) ?? NO_SHEET());
route("POST", `${BASE}/pi-sheets`, (req) => {
  const body = (req.body ?? {}) as { code?: string; name?: string; recipe?: RecipeDocument };
  if (!body.code || !/^[A-Z0-9][A-Z0-9_.-]{0,39}$/.test(body.code)) return reply.problem(400, "COM-0001", "The request is not valid.", { code: "Use upper-case letters, digits, '_', '.' or '-'." });
  if (piSheets.all().some((s) => s.code === body.code)) return reply.problem(409, "SF-0183", "A PI sheet with this code exists.");
  const now = new Date().toISOString();
  return reply.created(piSheets.insert({ ...sheetBase, id: crypto.randomUUID(), code: body.code, version: 1, name: body.name ?? body.code, status: "draft", recipe: body.recipe ?? assemblySheet(), rev: 1, createdAt: now, updatedAt: now, releasedBy: null, releasedAt: null }));
});
route("PUT", `${BASE}/pi-sheets/{id}`, (req) => {
  const row = piSheets.get(req.params.id);
  if (!row) return NO_SHEET();
  const body = (req.body ?? {}) as { name?: string; rev?: number; recipe?: RecipeDocument };
  if (row.status !== "draft") return reply.problem(409, "SF-0181", "Only a draft can be edited.");
  if (body.rev !== row.rev) return reply.problem(409, "SF-0182", "The PI sheet changed since you read it.");
  return reply.ok(piSheets.update(row.id, { name: body.name ?? row.name, recipe: body.recipe ?? row.recipe, rev: row.rev + 1, updatedAt: new Date().toISOString() }));
});
route("POST", `${BASE}/pi-sheets/{id}:release`, (req) => {
  const row = piSheets.get(req.params.id);
  if (!row) return NO_SHEET();
  const body = (req.body ?? {}) as { rev?: number };
  if (row.status !== "draft") return reply.problem(409, "SF-0181", "Only a draft can be released.");
  if (body.rev !== row.rev) return reply.problem(409, "SF-0182", "The PI sheet changed since you read it.");
  for (const other of piSheets.all()) if (other.code === row.code && other.status === "released") piSheets.update(other.id, { status: "obsolete", rev: other.rev + 1 });
  const now = new Date().toISOString();
  return reply.ok(piSheets.update(row.id, { status: "released", rev: row.rev + 1, releasedBy: "Jordan Avery", releasedAt: now, updatedAt: now }));
});
route("POST", `${BASE}/pi-sheets/{id}:new-version`, (req) => {
  const row = piSheets.get(req.params.id);
  if (!row) return NO_SHEET();
  if (row.status !== "released") return reply.problem(409, "SF-0181", "A new version is made from a released sheet.");
  if (piSheets.all().some((s) => s.code === row.code && s.status === "draft")) return reply.problem(409, "SF-0183", "The PI sheet has a draft already.");
  const now = new Date().toISOString();
  const version = Math.max(...piSheets.all().filter((s) => s.code === row.code).map((s) => s.version)) + 1;
  return reply.ok(piSheets.insert({ ...row, id: crypto.randomUUID(), version, status: "draft", rev: 1, createdAt: now, updatedAt: now, releasedBy: null, releasedAt: null }));
});
route("POST", `${BASE}/pi-sheets/{id}:obsolete`, (req) => {
  const row = piSheets.get(req.params.id);
  if (!row) return NO_SHEET();
  const body = (req.body ?? {}) as { rev?: number };
  if (row.status !== "released") return reply.problem(409, "SF-0181", "Only a released sheet can be made obsolete.");
  if (body.rev !== row.rev) return reply.problem(409, "SF-0182", "The PI sheet changed since you read it.");
  return reply.ok(piSheets.update(row.id, { status: "obsolete", rev: row.rev + 1, updatedAt: new Date().toISOString() }));
});

// PI categories (FR-25.15, D7): the groups the step library's templates are filed under.
const K_PI_CATEGORY = 389;
const piCategories = table<PICategory>([
  { id: uid(K_PI_CATEGORY, 1), code: "WEIGH", name: "Weighing and dosing", description: "Scale readings, tare and dosing checks.", active: true, version: 2, createdBy: ME.id, createdAt: at(-80), updatedAt: at(-20) },
  { id: uid(K_PI_CATEGORY, 2), code: "CLEAN", name: "Cleaning and line clearance", description: "Cleaning steps and the checks that follow them.", active: true, version: 1, createdBy: ME.id, createdAt: at(-80), updatedAt: at(-80) },
  { id: uid(K_PI_CATEGORY, 3), code: "CURE", name: "Curing and drying", description: "Timed holds and oven readouts.", active: true, version: 1, createdBy: ME.id, createdAt: at(-60), updatedAt: at(-60) },
  { id: uid(K_PI_CATEGORY, 4), code: "LEGACY", name: "Retired step groups", description: "", active: false, version: 3, createdBy: ME.id, createdAt: at(-300), updatedAt: at(-90) },
]);
const NO_PI_CATEGORY = () => reply.problem(404, "SF-0190", "PI category not found.");
route("GET", `${BASE}/pi-categories`, (req) => ({ items: piCategories.all().filter((c) => req.query.get("active") !== "true" || c.active).sort((a, b) => a.code.localeCompare(b.code)) }));
route("GET", `${BASE}/pi-categories/{id}`, (req) => {
  const row = piCategories.get(req.params.id);
  return row ? reply.versioned(row, row.version) : NO_PI_CATEGORY();
});
route("POST", `${BASE}/pi-categories`, (req) => {
  const body = (req.body ?? {}) as { code?: string; name?: string; description?: string; active?: boolean };
  const errors: Record<string, string> = {};
  if (!body.code || !/^[A-Z0-9][A-Z0-9_.-]{0,39}$/.test(body.code)) errors.code = "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'";
  if (!body.name?.trim()) errors.name = "must be 1 to 120 characters";
  if (Object.keys(errors).length > 0) return reply.problem(422, "COM-0001", "request failed validation", errors);
  if (piCategories.all().some((c) => c.code === body.code)) return reply.problem(409, "SF-0142", "code is already used");
  const now = new Date().toISOString();
  const row = piCategories.insert({ id: crypto.randomUUID(), code: body.code!, name: body.name!.trim(), description: body.description?.trim() ?? "", active: body.active !== false, version: 1, createdBy: ME.id, createdAt: now, updatedAt: now });
  return reply.versioned(row, 1, 201);
});
route("PUT", `${BASE}/pi-categories/{id}`, (req) => {
  const row = piCategories.get(req.params.id);
  if (!row) return NO_PI_CATEGORY();
  if (req.headers.get("If-Match") !== `"${row.version}"`) return reply.problem(412, "SF-0143", "PI category was changed by someone else; reload it and retry with its current version");
  const body = (req.body ?? {}) as { name?: string; description?: string; active?: boolean };
  if (!body.name?.trim()) return reply.problem(422, "COM-0001", "request failed validation", { name: "must be 1 to 120 characters" });
  const next = piCategories.update(row.id, { name: body.name.trim(), description: body.description?.trim() ?? "", active: body.active !== false, version: row.version + 1, updatedAt: new Date().toISOString() })!;
  return reply.versioned(next, next.version);
});

// PI step library (FR-25.15, D7): versioned reusable steps. A published version is immutable.
const K_PI_STEP = 395;
const stepBase = { createdBy: ME.id, updatedBy: ME.id };
const piSteps = table<PIStepTemplate>([
  { ...stepBase, id: uid(K_PI_STEP, 1), code: "CURE-HOLD", version: 2, name: "Cure hold", categoryId: uid(K_PI_CATEGORY, 3), kind: "timer", spec: { category: "timer", text: "Let the resin cure; do not move the fixture.", durationSeconds: 900, blocking: true }, status: "published", rev: 3, createdAt: at(-30), updatedAt: at(-10), publishedBy: ME.id, publishedAt: at(-10) },
  { ...stepBase, id: uid(K_PI_STEP, 2), code: "CURE-HOLD", version: 1, name: "Cure hold", categoryId: uid(K_PI_CATEGORY, 3), kind: "timer", spec: { category: "timer", text: "Let the resin cure.", durationSeconds: 600, blocking: true }, status: "superseded", rev: 3, createdAt: at(-60), updatedAt: at(-10), publishedBy: ME.id, publishedAt: at(-50) },
  { ...stepBase, id: uid(K_PI_STEP, 3), code: "NET-WEIGHT", version: 1, name: "Net weight", categoryId: uid(K_PI_CATEGORY, 1), kind: "calculation", spec: { category: "calculation", text: "Net weight of the charge.", expression: "gross - tare", var: "net" }, status: "published", rev: 2, createdAt: at(-40), updatedAt: at(-35), publishedBy: ME.id, publishedAt: at(-35) },
  { ...stepBase, id: uid(K_PI_STEP, 4), code: "OVEN-TEMP", version: 1, name: "Oven temperature readout", categoryId: uid(K_PI_CATEGORY, 3), kind: "machine-readout", spec: { category: "machine-readout", text: "", node: "ns=2;s=Oven1.Temp", value: 180, tolerance: 5, var: "oven_temp", characteristic: { name: "Oven temperature", type: "numeric", unit: "degC", min: 150, max: 210 } }, status: "published", rev: 2, createdAt: at(-20), updatedAt: at(-18), publishedBy: ME.id, publishedAt: at(-18) },
  { ...stepBase, id: uid(K_PI_STEP, 5), code: "TOO-HOT", version: 1, name: "Oven too hot?", categoryId: null, kind: "branch", spec: { category: "branch", text: "Is the oven above its limit?", condition: "oven_temp > 200" }, status: "draft", rev: 1, createdAt: at(-2), updatedAt: at(-2), publishedBy: null, publishedAt: null },
]);
const NO_PI_STEP = () => reply.problem(404, "SF-0192", "PI step template not found.");
const stale = (req: { headers: Headers }, row: PIStepTemplate) => req.headers.get("If-Match") !== `"${row.rev}"`;
route("GET", `${BASE}/pi-step-templates`, (req) => {
  const q = req.query;
  return {
    items: piSteps
      .all()
      .filter((t) => (!q.get("status") || t.status === q.get("status")) && (!q.get("code") || t.code === q.get("code")) && (!q.get("version") || String(t.version) === q.get("version")) && (!q.get("categoryId") || t.categoryId === q.get("categoryId")) && (!q.get("kind") || t.kind === q.get("kind")))
      .sort((a, b) => a.code.localeCompare(b.code) || b.version - a.version),
  };
});
route("GET", `${BASE}/pi-step-templates/{id}`, (req) => {
  const row = piSteps.get(req.params.id);
  return row ? reply.versioned(row, row.rev) : NO_PI_STEP();
});
route("POST", `${BASE}/pi-step-templates`, (req) => {
  const body = (req.body ?? {}) as { code?: string; name?: string; categoryId?: string | null; spec?: PIStepSpec };
  if (!body.code || !/^[A-Z0-9][A-Z0-9_.-]{0,39}$/.test(body.code)) return reply.problem(422, "COM-0001", "request failed validation", { code: "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'" });
  if (!body.spec || !body.name?.trim()) return reply.problem(422, "COM-0001", "request failed validation", { name: "must be 1 to 120 characters" });
  if (piSteps.all().some((t) => t.code === body.code)) return reply.problem(409, "SF-0194", "a PI step template with this code exists; make a new version of it instead");
  const now = new Date().toISOString();
  const row = piSteps.insert({ ...stepBase, id: crypto.randomUUID(), code: body.code, version: 1, name: body.name.trim(), categoryId: body.categoryId ?? null, kind: body.spec.category, spec: body.spec, status: "draft", rev: 1, createdAt: now, updatedAt: now, publishedBy: null, publishedAt: null });
  return reply.versioned(row, 1, 201);
});
route("PUT", `${BASE}/pi-step-templates/{id}`, (req) => {
  const row = piSteps.get(req.params.id);
  if (!row) return NO_PI_STEP();
  if (row.status !== "draft") return reply.problem(409, "SF-0193", `a ${row.status} version cannot be edited; make a new version of the template`);
  if (stale(req, row)) return reply.problem(412, "SF-0143", "PI step template was changed by someone else; reload it and retry with its current version");
  const body = (req.body ?? {}) as { name?: string; categoryId?: string | null; spec?: PIStepSpec };
  const next = piSteps.update(row.id, { name: body.name?.trim() || row.name, categoryId: body.categoryId ?? null, spec: body.spec ?? row.spec, kind: body.spec?.category ?? row.kind, rev: row.rev + 1, updatedAt: new Date().toISOString() })!;
  return reply.versioned(next, next.rev);
});
route("POST", `${BASE}/pi-step-templates/{id}:publish`, (req) => {
  const row = piSteps.get(req.params.id);
  if (!row) return NO_PI_STEP();
  if (row.status !== "draft") return reply.problem(409, "SF-0193", "only a draft can be published");
  if (stale(req, row)) return reply.problem(412, "SF-0143", "PI step template was changed by someone else; reload it and retry with its current version");
  for (const other of piSteps.all()) if (other.code === row.code && other.status === "published") piSteps.update(other.id, { status: "superseded", rev: other.rev + 1 });
  const now = new Date().toISOString();
  const next = piSteps.update(row.id, { status: "published", rev: row.rev + 1, publishedBy: ME.id, publishedAt: now, updatedAt: now })!;
  return reply.versioned(next, next.rev);
});
route("POST", `${BASE}/pi-step-templates/{id}:new-version`, (req) => {
  const row = piSteps.get(req.params.id);
  if (!row) return NO_PI_STEP();
  if (row.status !== "published") return reply.problem(409, "SF-0193", "a new version is made from the published version of a template");
  if (piSteps.all().some((t) => t.code === row.code && t.status === "draft")) return reply.problem(409, "SF-0194", "the template has a draft already; finish or publish it first");
  const now = new Date().toISOString();
  const version = Math.max(...piSteps.all().filter((t) => t.code === row.code).map((t) => t.version)) + 1;
  const made = piSteps.insert({ ...row, id: crypto.randomUUID(), version, status: "draft", rev: 1, createdAt: now, updatedAt: now, publishedBy: null, publishedAt: null });
  return reply.versioned(made, 1, 201);
});

// Machine readouts on the kiosk: the connector's latest reading of a node (the demo answers every node with a plausible value).
route("GET", `${BASE}/node-readings`, (req) => {
  const node = req.query.get("node") ?? "";
  if (!node) return reply.problem(400, "COM-0001", "request failed validation", { node: "is required" });
  return { node, value: "181.5", nodeType: "Double", readAt: new Date(Date.now() - 4000).toISOString(), ageSeconds: 4 };
});
route("POST", `${BASE}/edge-nodes/{id}/readings`, () => reply.noContent());

// PI execution records (FR-25.18): sealed runs of kiosk sheets. The demo seals nothing new; it serves two finished runs.
const K_PI_EXEC = 396;
const hex = (n: number) => (n.toString(16).padStart(2, "0")).repeat(32);
const execStep = (seq: number, over: Partial<PIExecutionStep>): PIExecutionStep => ({
  seq, key: `Assembly/Fit/Bearings/${seq}`, phasePath: "Pump assembly/Assembly/Fit/Bearings", kind: "instruction-text", text: "", template: null, status: "recorded", values: [], occurredAt: at(-1.2), signerUser: "", signerAuthTime: "",
  signatureMeaning: "", comment: "", deviation: false, deviationReasons: [], prevHash: hex(seq), hash: hex(seq + 1), ...over,
});
const execRows: PIExecutionDetail[] = [
  {
    id: uid(K_PI_EXEC, 1), recipeId: uid(234, 2), orderId: orders.all()[0]?.id ?? uid(234, 2), resourceId: resourceByCode("ASM-01").id, piSheetId: uid(388, 1), sheetCode: "PUMP-ASSY", sheetVersion: 2, executedBy: ME.id,
    startedAt: at(-1.3), completedAt: at(-1), stepCount: 4, deviationCount: 1, headHash: hex(5), recordHash: hex(9), createdAt: at(-1),
    steps: [
      execStep(1, { text: "Check the housing is clean.", status: "acknowledged", occurredAt: "" }),
      execStep(2, { kind: "process-data-request", text: "Record the bearing clearance.", values: [{ name: "Clearance", type: "numeric", value: "0.19", unit: "mm", min: "0.05", max: "0.15", outOfLimit: true }, { name: "comment", type: "text", value: "Gauge re-zeroed, second reading taken" }], comment: "Gauge re-zeroed, second reading taken", deviation: true, deviationReasons: ["Clearance is outside its limits"] }),
      execStep(3, { kind: "timer", text: "Let the sealant cure.", values: [{ name: "timer-seconds", type: "numeric", value: "600" }, { name: "timer-blocking", type: "boolean", value: "true" }] }),
      execStep(4, { kind: "signature-request", text: "Sign the fit.", signerUser: ME.id, signerAuthTime: at(-1.1), signatureMeaning: "verified", values: [{ name: "signature-meaning", type: "text", value: "verified" }] }),
    ],
  },
  {
    id: uid(K_PI_EXEC, 2), recipeId: uid(234, 1), orderId: orders.all()[1]?.id ?? uid(234, 1), resourceId: resourceByCode("TST-01").id, piSheetId: null, sheetCode: null, sheetVersion: null, executedBy: byUsername("omar.siddiqui").id,
    startedAt: at(-3.3), completedAt: at(-3), stepCount: 2, deviationCount: 0, headHash: hex(7), recordHash: hex(8), createdAt: at(-3),
    steps: [execStep(1, { kind: "machine-readout", text: "Oven temperature.", values: [{ name: "Oven temperature", type: "numeric", value: "181.5", unit: "degC" }, { name: "readout-source", type: "text", value: "machine" }] }), execStep(2, { text: "Release the load.", status: "acknowledged", occurredAt: "" })],
  },
];
const execHeader = ({ steps: _steps, ...row }: PIExecutionDetail): PIExecution => row;
route("GET", `${BASE}/pi-executions`, (req) => ({ items: execRows.filter((x) => (!req.query.get("orderId") || x.orderId === req.query.get("orderId")) && (!req.query.get("recipeId") || x.recipeId === req.query.get("recipeId"))).map(execHeader) }));
route("GET", `${BASE}/pi-executions/{id}`, (req) => execRows.find((x) => x.id === req.params.id) ?? reply.problem(404, "SF-0197", "PI execution record not found."));
route("POST", `${BASE}/pi-executions/{id}:verify`, (req) => {
  const row = execRows.find((x) => x.id === req.params.id);
  return row ? { valid: true, checkedSteps: row.stepCount, brokenAtSeq: null, reason: "", recordHash: row.recordHash } : reply.problem(404, "SF-0197", "PI execution record not found.");
});

// ---- Andon thresholds: the tenant's, and the Dallas site's own -----------------------------------------------------

let tenantAndon: AndonThresholds & { updatedBy?: string; updatedAt?: string } = { downtimeMinutes: 10, clearMinutes: 5, scrapPercent: 5, scrapMinCount: 10, updatedBy: ME.id, updatedAt: at(-40) };
const andonSites = table<AndonSiteSettings & { id: string }>([
  { id: DC.id, siteId: DC.id, downtimeMinutes: 20, clearMinutes: 10, scrapPercent: 8, scrapMinCount: 25, updatedBy: byUsername("omar.siddiqui").id, updatedAt: at(-12) },
]);
const andonSettingsView = (): AndonSettings => ({
  ...tenantAndon,
  default: false,
  sites: andonSites.all().map(({ id: _id, ...site }) => site),
});
route("GET", `${BASE}/andon-settings`, () => andonSettingsView());
route("PUT", `${BASE}/andon-settings`, ({ body }) => {
  const b = (body ?? {}) as Partial<AndonThresholds>;
  if (!b.downtimeMinutes || b.downtimeMinutes < 1 || b.downtimeMinutes > 240) return reply.problem(400, "COM-0001", "request failed validation", { downtimeMinutes: "must be 1 to 240" });
  tenantAndon = { downtimeMinutes: b.downtimeMinutes, clearMinutes: b.clearMinutes ?? 5, scrapPercent: b.scrapPercent ?? 5, scrapMinCount: b.scrapMinCount ?? 10, updatedBy: ME.id, updatedAt: at(0) };
  return reply.ok(andonSettingsView());
});
route("PUT", `${BASE}/andon-settings/sites/{siteId}`, ({ params, body }) => {
  const b = (body ?? {}) as Partial<AndonThresholds>;
  const row = { id: params.siteId, siteId: params.siteId, downtimeMinutes: b.downtimeMinutes ?? 10, clearMinutes: b.clearMinutes ?? 5, scrapPercent: b.scrapPercent ?? 5, scrapMinCount: b.scrapMinCount ?? 10, updatedBy: ME.id, updatedAt: at(0) };
  andonSites.insert(row);
  const { id: _id, ...out } = row;
  return reply.ok(out);
});
route("DELETE", `${BASE}/andon-settings/sites/{siteId}`, ({ params }) => (andonSites.remove(params.siteId) ? reply.noContent() : reply.problem(404, "SF-0187", "the site has no thresholds of its own")));

// ---- Edge fleet: the nodes, their configuration versions and certificate history -----------------------------------------

const K_EDGE = 233;
const K_CERT = 234;
const edgeNodes = table<EdgeNode>([
  {
    id: uid(K_EDGE, 1), siteId: PLANT.id, name: "Machining line gateway", platform: "k3s", ring: "all", connectorSubject: "edge-machining-line", status: "online", connectorVersion: "1.4.2",
    certSerial: "6F3A91C20B7D4E58", certExpiresAt: at(70), lastHeartbeatAt: at(-0.001), bufferFillPct: "4.00", machines: [{ machineCode: "M-101", state: "RUNNING" }, { machineCode: "M-102", state: "DOWN" }],
    appliedConfigVersion: 2, latestConfigVersion: 2, active: true, version: 3, createdBy: ME.id, createdAt: at(-200), updatedAt: at(-5),
  },
  {
    id: uid(K_EDGE, 2), siteId: PLANT.id, name: "Paint and test gateway", platform: "windows", ring: "canary", connectorSubject: "edge-paint-test", status: "online", connectorVersion: "1.5.0-rc1",
    certSerial: "2B8C40D91E6F7A03", certExpiresAt: at(20), lastHeartbeatAt: at(-0.002), bufferFillPct: "11.50", machines: [{ machineCode: "M-104", state: "RUNNING" }, { machineCode: "M-105", state: "IDLE" }],
    appliedConfigVersion: 1, latestConfigVersion: 2, active: true, version: 2, createdBy: ME.id, createdAt: at(-90), updatedAt: at(-3),
  },
] as EdgeNode[]);
route("GET", `${BASE}/edge-nodes`, () => ({ items: edgeNodes.all() }));
route("GET", `${BASE}/edge-nodes/{id}`, ({ params }) => edgeNodes.get(params.id) ?? reply.problem(404, "SF-0150", "No edge node with that id."));
route("GET", `${BASE}/edge-nodes/{id}/config-versions`, ({ params }) => {
  const sha = (n: number) => `${n}`.repeat(64).slice(0, 64);
  const row = (version: number, applied: boolean): EdgeConfigVersion => ({
    id: uid(K_EDGE, 100 + version), edgeId: params.id, version, document: { version, writeRules: [{ node: "ns=2;s=Press.Setpoint", min: 0, max: 90 }] }, documentSha256: sha(version),
    publishedBy: ME.id, publishedAt: at(-30 + version), appliedAt: applied ? at(-29 + version) : null,
  });
  return { items: [row(2, params.id === uid(K_EDGE, 1)), row(1, true)] };
});

const certs = table<EdgeCertificate & { id: string; edgeId: string }>(
  [1, 2].flatMap((n) => {
    const edgeId = uid(K_EDGE, n);
    const subject = n === 1 ? "edge-machining-line" : "edge-paint-test";
    return [
      { id: uid(K_CERT, n * 10 + 1), edgeId, serial: n === 1 ? "6F3A91C20B7D4E58" : "2B8C40D91E6F7A03", subject, reason: "renew" as const, notBefore: at(-20), notAfter: at(n === 1 ? 70 : 20), issuedAt: at(-20), fingerprint: (n === 1 ? "a1" : "b2").repeat(32), current: true },
      { id: uid(K_CERT, n * 10 + 2), edgeId, serial: n === 1 ? "11C0FFEE5A5A0001" : "22D0DEED5B5B0002", subject, reason: "enroll" as const, notBefore: at(-110), notAfter: at(-20 + 90), issuedAt: at(-110), fingerprint: (n === 1 ? "c3" : "d4").repeat(32), current: false, revokedAt: at(-20), revokedBy: ME.id, revokeReason: "Replaced when the certificate was renewed" },
    ];
  }),
);
route("GET", `${BASE}/edge-nodes/{id}/certificates`, ({ params }) => ({
  items: certs.all().filter((c) => c.edgeId === params.id).sort((a, b) => b.issuedAt.localeCompare(a.issuedAt)).map(({ id: _id, edgeId: _edge, ...c }) => c),
}));
route("POST", `${BASE}/edge-nodes/{id}/certificates/{serial}:revoke`, ({ params, body }) => {
  const row = certs.all().find((c) => c.edgeId === params.id && c.serial === params.serial);
  if (!row) return reply.problem(404, "SF-0185", "No certificate with that serial.");
  const reason = String((body as { reason?: string } | undefined)?.reason ?? "").trim();
  if (!reason) return reply.problem(400, "COM-0001", "request failed validation", { reason: "is required" });
  if (!row.revokedAt) {
    certs.update(row.id, { revokedAt: at(0), revokedBy: ME.id, revokeReason: reason });
    if (row.current) edgeNodes.update(params.id, { connectorSubject: null, certSerial: null, certExpiresAt: null });
  }
  const { id: _id, edgeId: _edge, ...out } = certs.get(row.id)!;
  return reply.ok(out);
});
