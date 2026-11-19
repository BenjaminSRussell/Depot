import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { type AiPolicyStore, type AiPolicyWrite } from "../src/api/aiDataPolicy.js";
import { ApiError } from "../src/api/principal.js";
import {
  AI_POLICY_MODULES,
  type AiDataPolicy,
  applyPatch,
  decide,
  defaultPolicy,
  parsePolicyPatch,
} from "../src/domain/aiDataPolicy.js";
import { AgentError } from "../src/errors.js";
import { createAiPolicyGuard } from "../src/runtime/aiPolicyGuard.js";
import { headers, OTHER, serve, T1, USER, MemoryStore } from "./surface-fakes.js";

const URL_PATH = "/api/v1/agents/ai-data-policy";
const WRITER = "aipolicy.read,aipolicy.write,tenant.administer";

class MemoryPolicies implements AiPolicyStore {
  readonly policies = new Map<string, AiDataPolicy>();
  fail = false;
  writes = 0;

  async read(tenantId: string): Promise<AiDataPolicy> {
    if (this.fail) throw new Error("database down");
    return this.policies.get(tenantId) ?? defaultPolicy();
  }

  async write(input: AiPolicyWrite): Promise<AiDataPolicy> {
    const current = await this.read(input.tenantId);
    if (current.version !== input.expectedVersion) throw new ApiError(412, "AGT-0412", "stale");
    const applied = applyPatch(current, input.patch);
    if (!applied.changed) return current;
    this.writes += 1;
    const next = { version: current.version + 1, modules: applied.modules };
    this.policies.set(input.tenantId, next);
    return next;
  }
}

async function api(run: (base: string, policies: MemoryPolicies) => Promise<void>): Promise<void> {
  const policies = new MemoryPolicies();
  const srv = await serve({ approvals: new MemoryStore(), aiPolicy: policies });
  try {
    await run(srv.base, policies);
  } finally {
    await srv.close();
  }
}

const get = (base: string, duties: string, tenant = T1) => fetch(`${base}${URL_PATH}`, { headers: headers(duties, USER, tenant) });
const put = (base: string, duties: string, body: unknown, ifMatch: string | null = '"1"', tenant = T1) =>
  fetch(`${base}${URL_PATH}`, {
    method: "PUT",
    headers: { ...headers(duties, USER, tenant), ...(ifMatch === null ? {} : { "if-match": ifMatch }) },
    body: JSON.stringify(body),
  });

describe("the AI data policy domain", () => {
  it("default denies P3 and CUI everywhere, and P2 until it is allowed", () => {
    const policy = defaultPolicy();
    expect(policy.version).toBe(1);
    for (const m of AI_POLICY_MODULES) {
      expect(policy.modules[m]).toEqual({ P0: true, P1: true, P2: false, P3: false, CUI: false });
    }
  });

  it("applies a patch to the named cells only and flags loosening", () => {
    const applied = applyPatch(defaultPolicy(), { modules: { ap: { P2: true } } });
    expect(applied.modules.ap.P2).toBe(true);
    expect(applied.modules.ar.P2).toBe(false);
    expect(applied).toMatchObject({ changed: true, loosening: true });
    const tightened = applyPatch({ version: 2, modules: applied.modules }, { modules: { ap: { P2: false, P0: false } } });
    expect(tightened).toMatchObject({ changed: true, loosening: false });
  });

  it("reports a patch that sets the current values as unchanged", () => {
    expect(applyPatch(defaultPolicy(), { modules: { ap: { P2: false, P0: true } } })).toMatchObject({ changed: false, loosening: false });
  });

  it("refuses allowing P3 and CUI", () => {
    for (const c of ["P3", "CUI"] as const) {
      expect(() => applyPatch(defaultPolicy(), { modules: { ar: { [c]: true } } })).toThrow(AgentError);
    }
  });

  it("decides by module and class, denies the unknown, and names the policy version", () => {
    const policy = { ...defaultPolicy(), version: 7 };
    expect(decide(policy, "ap", "P1")).toMatchObject({ allowed: true, policyVersion: 7 });
    expect(decide(policy, "ap", "P3")).toMatchObject({ allowed: false, policyVersion: 7 });
    expect(decide(policy, "hr", "P0").allowed).toBe(false);
    expect(decide(policy, "ap", "P9").allowed).toBe(false);
    expect(decide(policy, "__proto__", "constructor").allowed).toBe(false);
  });

  it.each([
    ["null", null],
    ["an array", []],
    ["empty", {}],
    ["no modules", { modules: {} }],
    ["empty module", { modules: { ap: {} } }],
    ["unknown key", { modules: { ap: { P2: true } }, extra: 1 }],
    ["unknown module", { modules: { hr: { P0: true } } }],
    ["unknown class", { modules: { ap: { P9: true } } }],
    ["modules as a string", { modules: "ap" }],
    ["module as an array", { modules: { ap: [] } }],
    ["cell as a string", { modules: { ap: { P2: "true" } } }],
    ["cell as a number", { modules: { ap: { P2: 1 } } }],
    ["cell as null", { modules: { ap: { P2: null } } }],
    ["cell as an object", { modules: { ap: { P2: {} } } }],
    ["reason as a number", { modules: { ap: { P2: true } }, reason: 5 }],
    ["reason too long", { modules: { ap: { P2: true } }, reason: "x".repeat(501) }],
  ])("rejects a malformed patch: %s", (_name, body) => {
    expect(() => parsePolicyPatch(body)).toThrow(ApiError);
  });

  it("accepts a reason of 500 characters", () => {
    expect(parsePolicyPatch({ modules: { ap: { P2: true } }, reason: "x".repeat(500) }).reason).toHaveLength(500);
  });
});

describe("GET and PUT /api/v1/agents/ai-data-policy", () => {
  it("answers a tenant that never set a policy with version 1 and P3 and CUI denied", async () => {
    await api(async (base) => {
      const res = await get(base, "aipolicy.read");
      expect(res.status).toBe(200);
      expect(res.headers.get("etag")).toBe('"1"');
      const body = (await res.json()) as AiDataPolicy;
      expect(body.version).toBe(1);
      for (const m of AI_POLICY_MODULES) expect(body.modules[m]).toMatchObject({ P3: false, CUI: false });
    });
  });

  it("refuses the missing bearer token, the missing duties and an unwired service", async () => {
    await api(async (base) => {
      expect((await fetch(`${base}${URL_PATH}`)).status).toBe(401);
      expect((await get(base, "agents.admin")).status).toBe(403);
      expect((await put(base, "aipolicy.read,tenant.administer", { modules: { ap: { P2: true } } })).status).toBe(403);
      expect((await put(base, "aipolicy.write", { modules: { ap: { P2: true } } })).status).toBe(403);
    });
    const bare = await serve({ approvals: new MemoryStore() });
    try {
      const res = await get(bare.base, "aipolicy.read");
      expect(res.status).toBe(404);
      expect(((await res.json()) as { code: string }).code).toBe("AGT-0404");
    } finally {
      await bare.close();
    }
  });

  it("answers 503 AGT-0216 when the policy cannot be read", async () => {
    await api(async (base, policies) => {
      policies.fail = true;
      const res = await get(base, "aipolicy.read");
      expect(res.status).toBe(503);
      expect(((await res.json()) as { code: string }).code).toBe("AGT-0216");
    });
  });

  it("allows P2 for ap, bumps the version and replays the same patch without a new version", async () => {
    await api(async (base, policies) => {
      const res = await put(base, WRITER, { modules: { ap: { P2: true } }, reason: "pilot" });
      expect(res.status).toBe(200);
      expect(res.headers.get("etag")).toBe('"2"');
      expect(((await res.json()) as AiDataPolicy).modules.ap.P2).toBe(true);
      const replay = await put(base, WRITER, { modules: { ap: { P2: true } } }, '"2"');
      expect(replay.status).toBe(200);
      expect(((await replay.json()) as AiDataPolicy).version).toBe(2);
      expect(policies.writes).toBe(1);
    });
  });

  it("requires If-Match and refuses a stale or unparseable one", async () => {
    await api(async (base) => {
      const patch = { modules: { ap: { P2: true } } };
      const missing = await put(base, WRITER, patch, null);
      expect(missing.status).toBe(428);
      expect(((await missing.json()) as { code: string }).code).toBe("AGT-0428");
      for (const stale of ['"2"', '"0"', "6"]) expect((await put(base, WRITER, patch, stale)).status).toBe(412);
      for (const bad of ["abc", '""', "1.5", "-1"]) expect((await put(base, WRITER, patch, bad)).status).toBe(400);
    });
  });

  it("refuses allowing P3 or CUI with 422 AGT-0217 and stores nothing", async () => {
    await api(async (base, policies) => {
      for (const c of ["P3", "CUI"]) {
        const res = await put(base, WRITER, { modules: { ap: { P2: true, [c]: true } } });
        expect(res.status).toBe(422);
        expect(((await res.json()) as { code: string }).code).toBe("AGT-0217");
      }
      expect(policies.writes).toBe(0);
    });
  });

  it("answers 400 for a malformed body", async () => {
    await api(async (base) => {
      for (const body of [{}, [], null, { modules: { ap: { P2: "yes" } } }, { modules: { hr: { P0: true } } }]) {
        expect((await put(base, WRITER, body)).status).toBe(400);
      }
      const empty = await fetch(`${base}${URL_PATH}`, { method: "PUT", headers: { ...headers(WRITER), "if-match": '"1"' } });
      expect(empty.status).toBe(400);
    });
  });

  it("keeps each tenant's policy to itself", async () => {
    await api(async (base) => {
      await put(base, WRITER, { modules: { ap: { P2: true } } });
      const other = (await (await get(base, "aipolicy.read", OTHER)).json()) as AiDataPolicy;
      expect(other.version).toBe(1);
      expect(other.modules.ap.P2).toBe(false);
    });
  });
});

describe("the AI policy guard", () => {
  const TENANT_B = "00000000-0000-7000-8000-0000000000b2";

  it("fails closed when the policy cannot be read, and reports the error", async () => {
    const seen: unknown[] = [];
    const guard = createAiPolicyGuard({ read: async () => Promise.reject(new Error("down")), onError: (e) => seen.push(e) });
    await expect(guard.check({ tenantId: T1, module: "ap", dataClass: "P0" })).rejects.toMatchObject({ code: "AGT-0216" });
    expect(seen).toHaveLength(1);
  });

  it("fails closed when the read does not answer in time", async () => {
    const guard = createAiPolicyGuard({ read: () => new Promise<AiDataPolicy>(() => undefined), readTimeoutMs: 5 });
    await expect(guard.check({ tenantId: T1, module: "ap", dataClass: "P0" })).rejects.toMatchObject({ code: "AGT-0216" });
  });

  it("does not serve a stale policy after a failed refresh", async () => {
    let t = 0;
    let down = false;
    const guard = createAiPolicyGuard({
      read: async () => (down ? Promise.reject(new Error("down")) : defaultPolicy()),
      now: () => t,
      ttlMs: 100,
    });
    await guard.check({ tenantId: T1, module: "ap", dataClass: "P0" });
    down = true;
    t = 101;
    await expect(guard.check({ tenantId: T1, module: "ap", dataClass: "P0" })).rejects.toMatchObject({ code: "AGT-0216" });
  });

  it("denies the unknown and a tenant id that is not a UUID without reading", async () => {
    let reads = 0;
    const guard = createAiPolicyGuard({ read: async () => (reads++, defaultPolicy()) });
    expect((await guard.check({ tenantId: "nope", module: "ap", dataClass: "P0" })).allowed).toBe(false);
    expect((await guard.check({ tenantId: T1, module: "hr", dataClass: "P0" })).allowed).toBe(false);
    expect((await guard.check({ tenantId: T1, module: "ap", dataClass: "P9" })).allowed).toBe(false);
    expect(reads).toBe(1);
  });

  it("sees a write at once on the writing pod and after the TTL on another", async () => {
    const policies = new MemoryPolicies();
    let t = 0;
    const guard = createAiPolicyGuard({ read: (id) => policies.read(id), now: () => t, ttlMs: 2_000 });
    const check = () => guard.check({ tenantId: T1, module: "ap", dataClass: "P2" });
    expect((await check()).allowed).toBe(false);
    await policies.write({ tenantId: T1, userId: USER, expectedVersion: 1, patch: { modules: { ap: { P2: true } } }, now: new Date() });
    t = 1_999;
    expect(await check()).toMatchObject({ allowed: false, policyVersion: 1 });
    guard.invalidate(T1);
    expect(await check()).toMatchObject({ allowed: true, policyVersion: 2 });
    const second = createAiPolicyGuard({ read: (id) => policies.read(id), now: () => t, ttlMs: 2_000 });
    expect((await second.check({ tenantId: T1, module: "ap", dataClass: "P2" })).allowed).toBe(true);
  });

  it("records each tenant's own policy version on its results", async () => {
    const policies = new MemoryPolicies();
    await policies.write({ tenantId: T1, userId: USER, expectedVersion: 1, patch: { modules: { ap: { P2: true } } }, now: new Date() });
    const guard = createAiPolicyGuard({ read: (id) => policies.read(id) });
    expect(await guard.check({ tenantId: T1, module: "ap", dataClass: "P2" })).toMatchObject({ allowed: true, policyVersion: 2 });
    expect(await guard.check({ tenantId: TENANT_B, module: "ap", dataClass: "P2" })).toMatchObject({ allowed: false, policyVersion: 1 });
  });
});

describe("the module list", () => {
  it("covers every domain the services expose as a read tool", () => {
    const services = fileURLToPath(new URL("../..", import.meta.url));
    const domains = new Set<string>();
    for (const dir of readdirSync(services)) {
      const spec = join(services, dir, "api", "openapi.yaml");
      if (!existsSync(spec)) continue;
      for (const m of readFileSync(spec, "utf8").matchAll(/x-depot-tool:\s*["']?depot\.([a-z0-9]+)\./g)) domains.add(m[1] ?? "");
    }
    expect(domains.size).toBeGreaterThan(0);
    expect([...domains].filter((d) => !(AI_POLICY_MODULES as readonly string[]).includes(d))).toEqual([]);
  });
});
