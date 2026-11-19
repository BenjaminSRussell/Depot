import { test as base, type Locator, type Request } from "@playwright/test";

export { expect } from "@playwright/test";

export type SessionEntry = { t: number; method: string; url: string; postData?: string | null };
export type SessionEvent = { type: "approve"; t: number };
export type Session = { entries: SessionEntry[]; events: SessionEvent[] };
export type Violation = { t: number; method: string; url: string; reason: string };
export type Mode = "no-writes" | "after-approval";
export type Verdict = { startedAt: number; approvedAt: number | undefined; violations: Violation[] };
export type AllowRule = { id: string; method: "POST"; path: RegExp };

const ORIGIN = "http://write-guard.invalid";
const DOMAIN_PREFIX = "/api/v1/";

export const WRITE_ALLOWLIST: readonly AllowRule[] = [
  { id: "copilot-conversation-create", method: "POST", path: /^\/api\/v1\/agents\/copilot\/conversations$/ },
  { id: "copilot-message-send", method: "POST", path: /^\/api\/v1\/agents\/copilot\/conversations\/[^/]+\/messages$/ },
  { id: "briefing-generate", method: "POST", path: /^\/api\/v1\/agents\/briefings:generate$/ },
  { id: "briefing-mark-read", method: "POST", path: /^\/api\/v1\/agents\/briefings\/[^/]+:mark-read$/ },
  { id: "proposal-create", method: "POST", path: /^\/api\/v1\/agents\/runs$/ },
  { id: "copilot-chat-interim", method: "POST", path: /^\/api\/v1\/copilot\/chat$/ },
];

const DECLINE_PATHS: readonly RegExp[] = [
  /^\/api\/v1\/agents\/approvals\/[^/]+:decide$/,
  /^\/api\/v1\/agents\/runs\/[^/]+\/actions\/[^/]+\/decision$/,
];

type Kind = "read" | "allowed" | "write" | "invalid";

function isDecline(postData: string | null | undefined): boolean {
  if (!postData) return false;
  try {
    const body: unknown = JSON.parse(postData);
    return typeof body === "object" && body !== null && (body as { decision?: unknown }).decision === "REJECTED";
  } catch {
    return false;
  }
}

export function classify(entry: SessionEntry): Kind {
  if (typeof entry.url !== "string" || entry.url.trim() === "") return "invalid";
  let pathname: string;
  try {
    pathname = new URL(entry.url, ORIGIN).pathname;
  } catch {
    return "invalid";
  }
  const method = entry.method.toUpperCase();
  if (method === "GET" || !pathname.startsWith(DOMAIN_PREFIX)) return "read";
  if (WRITE_ALLOWLIST.some((rule) => rule.method === method && rule.path.test(pathname))) return "allowed";
  if (method === "POST" && DECLINE_PATHS.some((path) => path.test(pathname)) && isDecline(entry.postData)) return "allowed";
  return "write";
}

export function isDomainWrite(entry: SessionEntry): boolean {
  const kind = classify(entry);
  return kind === "write" || kind === "invalid";
}

function finiteTime(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`writeGuard: ${label} must be a finite number of epoch milliseconds`);
  return value;
}

export function parseSession(raw: unknown): Session {
  const input = raw as { entries?: unknown; events?: unknown } | null;
  if (!input || !Array.isArray(input.entries) || !Array.isArray(input.events)) throw new Error("writeGuard: session needs entries and events arrays");
  const entries = input.entries.map((item: SessionEntry, index) => {
    finiteTime(item?.t, `entries[${index}].t`);
    if (typeof item.method !== "string" || typeof item.url !== "string") throw new Error(`writeGuard: entries[${index}] needs string method and url`);
    return item;
  });
  const events = input.events.map((item: SessionEvent, index) => {
    if (item?.type !== "approve") throw new Error(`writeGuard: events[${index}] has unknown type ${String(item?.type)}`);
    if (finiteTime(item.t, `events[${index}].t`) > Date.now()) throw new Error(`writeGuard: events[${index}].t is in the future`);
    return item;
  });
  return { entries, events };
}

export function evaluateSession(raw: unknown, mode: Mode): Verdict {
  const session = parseSession(raw);
  const approveTimes = session.events.map((event) => event.t);
  const approvedAt = approveTimes.length > 0 ? Math.min(...approveTimes) : undefined;
  const times = [...session.entries.map((entry) => entry.t), ...approveTimes];
  const violations: Violation[] = [];
  for (const entry of session.entries) {
    const kind = classify(entry);
    if (kind === "read" || kind === "allowed") continue;
    if (mode === "after-approval" && approvedAt !== undefined && entry.t >= approvedAt) continue;
    const reason =
      kind === "invalid"
        ? "unparseable request url"
        : mode === "no-writes"
          ? "domain write outside the allowlist"
          : approvedAt === undefined
            ? "domain write with no approval event"
            : "domain write before the approval event";
    violations.push({ t: entry.t, method: entry.method.toUpperCase(), url: entry.url, reason });
  }
  return { startedAt: times.length > 0 ? Math.min(...times) : 0, approvedAt, violations };
}

export function describeViolations(verdict: Verdict): string {
  const lines = verdict.violations.map(
    (v) => `${v.method} ${v.url} at ${new Date(v.t).toISOString()} (+${v.t - verdict.startedAt}ms): ${v.reason}`,
  );
  return `writeGuard: ${lines.length} unapproved domain write(s)\n${lines.join("\n")}`;
}

function assertClean(verdict: Verdict): void {
  if (verdict.violations.length > 0) throw new Error(describeViolations(verdict));
}

export type WriteGuard = {
  approvedAt: number | undefined;
  session(): Session;
  expectNoWrites(): void;
  expectWritesOnlyAfterApproval(approveEvent: Locator): Promise<void>;
};

export const test = base.extend<{ writeGuard: WriteGuard }>({
  writeGuard: async ({ page }, use) => {
    const session: Session = { entries: [], events: [] };
    const record = (request: Request): void => {
      session.entries.push({ t: Date.now(), method: request.method(), url: request.url(), postData: request.postData() });
    };
    page.on("request", record);
    const guard: WriteGuard = {
      approvedAt: undefined,
      session: () => structuredClone(session),
      expectNoWrites: () => assertClean(evaluateSession(session, "no-writes")),
      expectWritesOnlyAfterApproval: async (approveEvent) => {
        guard.approvedAt = Date.now();
        session.events.push({ type: "approve", t: guard.approvedAt });
        await approveEvent.click();
        await page.waitForLoadState("networkidle");
        assertClean(evaluateSession(session, "after-approval"));
      },
    };
    await use(guard);
    page.off("request", record);
  },
});
