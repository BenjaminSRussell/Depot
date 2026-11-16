// Audit policies (/api/v1/compliance/audit-policies) for the demo: the rules an audit run names, and the run itself over a
// pasted document window. The run applies keyword, amount-limit and duplicate rules; the other rule types find nothing.
import type { AuditFinding, AuditPolicy, AuditSpec } from "../../../modules/compliance/api/client";
import { reply, route } from "../../backend";
import { at, table, uid } from "../../store";
import { ME } from "./people";

const BASE = "/api/v1/compliance";
const K_POLICY = 911;

const policies = table<AuditPolicy & { id: string }>([
  { id: uid(K_POLICY, 1), policyId: uid(K_POLICY, 1), code: "GIFT-CARDS", name: "Gift cards and alcohol in expenses", ruleType: "KEYWORD", spec: { type: "KEYWORD", keywords: ["gift card", "alcohol"] }, active: true, version: 2, createdBy: ME.username, createdAt: at(-60), updatedAt: at(-12) },
  { id: uid(K_POLICY, 2), policyId: uid(K_POLICY, 2), code: "MEAL-LIMITS", name: "Meal and travel limits", ruleType: "THRESHOLD", spec: { type: "THRESHOLD", currency: "USD", limits: { MEALS: "200", TRAVEL: "1500" } }, active: true, version: 1, createdBy: ME.username, createdAt: at(-45), updatedAt: at(-45) },
  { id: uid(K_POLICY, 3), policyId: uid(K_POLICY, 3), code: "DUP-INVOICES", name: "Duplicate vendor invoices", ruleType: "DUPLICATE", spec: { type: "DUPLICATE" }, active: true, version: 1, createdBy: ME.username, createdAt: at(-30), updatedAt: at(-30) },
  { id: uid(K_POLICY, 4), policyId: uid(K_POLICY, 4), code: "WEEKEND-POSTINGS", name: "Weekend postings (retired)", ruleType: "WEEKEND_HOLIDAY", spec: { type: "WEEKEND_HOLIDAY" }, active: false, version: 1, createdBy: ME.username, createdAt: at(-90), updatedAt: at(-70) },
]);

const view = (p: AuditPolicy & { id: string }): AuditPolicy => {
  const { id: _id, ...rest } = p;
  return rest;
};

route("GET", `${BASE}/audit-policies`, ({ query }) => {
  const q = (query.get("q") ?? "").trim().toLowerCase();
  const items = policies
    .all()
    .filter((p) => (query.get("active") !== "true" || p.active) && (!query.get("code") || p.code === query.get("code")!.toUpperCase()) && (q === "" || p.code.toLowerCase().includes(q) || p.name.toLowerCase().includes(q)))
    .sort((a, b) => a.code.localeCompare(b.code))
    .map(view);
  return { items: items.slice(0, Number(query.get("limit") ?? 100)) };
});

route("POST", `${BASE}/audit-policies`, ({ body }) => {
  const input = body as { code: string; name: string; spec: AuditSpec };
  const code = (input.code ?? "").trim().toUpperCase();
  if (policies.all().some((p) => p.code === code)) return reply.problem(409, "AUD-0721", "an audit policy with this code already exists");
  const id = crypto.randomUUID();
  const row = policies.insert({ id, policyId: id, code, name: input.name.trim(), ruleType: input.spec.type, spec: input.spec, active: true, version: 1, createdBy: ME.username, createdAt: at(0), updatedAt: at(0) });
  return reply.versioned(view(row), 1, 201);
});

route("GET", `${BASE}/audit-policies/{id}`, ({ params }) => {
  const p = policies.get(params.id);
  return p ? reply.versioned(view(p), p.version) : reply.problem(404, "AUD-0720", "no audit policy with that id");
});

route("PATCH", `${BASE}/audit-policies/{id}`, ({ params, body, headers }) => {
  const p = policies.get(params.id);
  if (!p) return reply.problem(404, "AUD-0720", "no audit policy with that id");
  if (headers.get("If-Match") !== `"${p.version}"`) return reply.problem(412, "SOD-1109", "the audit policy changed; reload it");
  const patch = body as { name?: string; active?: boolean; spec?: AuditSpec };
  const next = policies.update(p.id, {
    ...(patch.name !== undefined ? { name: patch.name.trim() } : {}),
    ...(patch.active !== undefined ? { active: patch.active } : {}),
    ...(patch.spec ? { spec: patch.spec, ruleType: patch.spec.type, version: p.version + 1 } : {}),
    updatedAt: at(0),
  })!;
  return reply.versioned(view(next), next.version);
});

interface Doc {
  id: string;
  partyId?: string;
  category?: string;
  amount?: string;
  currency?: string;
  number?: string;
  texts?: string[];
}

function findings(policy: AuditPolicy, docs: Doc[]): AuditFinding[] {
  const key = (id: string) => `audit:${policy.policyId}:v${policy.version}:${id}`;
  const out: AuditFinding[] = [];
  const spec = policy.spec;
  if (spec.type === "KEYWORD") {
    for (const d of docs) {
      const hits = (spec.keywords ?? []).filter((k) => (d.texts ?? []).some((t) => t.toLowerCase().includes(k.toLowerCase())));
      if (hits.length > 0) out.push({ key: key(d.id), ruleType: "KEYWORD", documentId: d.id, score: 100, evidence: { keywords: hits.join(",") } });
    }
  } else if (spec.type === "THRESHOLD") {
    for (const d of docs) {
      const limit = Number(spec.limits?.[d.category ?? ""] ?? NaN);
      if (!Number.isNaN(limit) && Number(d.amount ?? 0) > limit)
        out.push({ key: key(d.id), ruleType: "THRESHOLD", documentId: d.id, score: 100, evidence: { category: d.category ?? "", amount: d.amount ?? "", limit: String(limit), pctOfLimit: String(Math.round((Number(d.amount) / limit) * 100)) } });
    }
  } else if (spec.type === "DUPLICATE") {
    const seen = new Map<string, string>();
    for (const d of docs) {
      const k = `${d.partyId}|${d.currency}|${d.amount}`;
      const earlier = seen.get(k);
      if (earlier) out.push({ key: key(d.id), ruleType: "DUPLICATE", documentId: d.id, score: 100, evidence: { vendorId: d.partyId ?? "", duplicateOf: earlier } });
      else seen.set(k, d.id);
    }
  }
  return out;
}

route("POST", `${BASE}/audit-policies:evaluate`, ({ body }) => {
  const input = body as { policyId: string; documents?: Doc[] };
  const policy = policies.get(input.policyId);
  if (!policy) return reply.problem(404, "AUD-0720", "no audit policy with that id");
  if (!policy.active) return reply.problem(422, "AUD-0722", "this audit policy is retired; run another policy");
  const found = findings(view(policy), input.documents ?? []);
  return { policyVersion: policy.version, count: found.length, findings: found, ...(found.length > 0 ? { code: "AUD-0701" } : {}) };
});
