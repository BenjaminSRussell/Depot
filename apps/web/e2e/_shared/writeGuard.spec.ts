import { classify, describeViolations, evaluateSession, expect, isDomainWrite, parseSession, test, WRITE_ALLOWLIST, type Session } from "./writeGuard";
import compliant from "./fixtures/compliant.json" with { type: "json" };
import declined from "./fixtures/declined.json" with { type: "json" };
import navigationOnly from "./fixtures/navigation-only.json" with { type: "json" };
import violating from "./fixtures/violating.json" with { type: "json" };

const BASE = "https://app.depot.example";
const T0 = 1767000000000;
const INVOICE_URL = `${BASE}/api/v1/ap/invoices`;
const entry = (method: string, path: string, postData?: string | null, t = T0) => ({ t, method, url: `${BASE}${path}`, postData });
const session = (entries: Session["entries"], events: Session["events"] = []): Session => ({ entries, events });

test.describe("write guard allowlist", () => {
  test("is exactly the documented list of POST routes", () => {
    expect(WRITE_ALLOWLIST.map((rule) => `${rule.id} ${rule.method}`)).toEqual([
      "copilot-conversation-create POST",
      "copilot-message-send POST",
      "briefing-generate POST",
      "briefing-mark-read POST",
      "proposal-create POST",
      "copilot-chat-interim POST",
    ]);
  });

  test("each rule admits its route and nothing broader", () => {
    const samples = [
      "/api/v1/agents/copilot/conversations",
      "/api/v1/agents/copilot/conversations/c-1/messages",
      "/api/v1/agents/briefings:generate",
      "/api/v1/agents/briefings/b-1:mark-read",
      "/api/v1/agents/runs",
      "/api/v1/copilot/chat",
    ];
    samples.forEach((path, index) => {
      expect(WRITE_ALLOWLIST[index].path.test(path)).toBe(true);
      expect(WRITE_ALLOWLIST.filter((rule) => rule.path.test(`${path}/extra`))).toEqual([]);
    });
    expect(classify(entry("PUT", "/api/v1/agents/runs"))).toBe("write");
    expect(classify(entry("POST", "/api/v1/agents/runs/r-1"))).toBe("write");
  });
});

test.describe("recorded sessions", () => {
  test("a domain write before any approval event fails and names method, url and timestamp", () => {
    const verdict = evaluateSession(violating, "after-approval");
    expect(verdict.violations).toEqual([
      { t: T0 + 3000, method: "POST", url: INVOICE_URL, reason: "domain write before the approval event" },
    ]);
    const message = describeViolations(verdict);
    expect(message).toContain("POST");
    expect(message).toContain(INVOICE_URL);
    expect(message).toContain(new Date(T0 + 3000).toISOString());
    expect(message).toContain("+3000ms");
  });

  test("a compliant act session passes and records the approval time", () => {
    const verdict = evaluateSession(compliant, "after-approval");
    expect(verdict.violations).toEqual([]);
    expect(verdict.approvedAt).toBe(T0 + 6000);
  });

  test("a compliant act session still fails the no-writes mode", () => {
    expect(evaluateSession(compliant, "no-writes").violations.map((v) => v.url)).toEqual([
      `${BASE}/api/v1/agents/approvals/a-1:decide`,
      INVOICE_URL,
    ]);
  });

  test("declined and expired sessions fail on any domain write", () => {
    expect(evaluateSession(declined.declined, "after-approval").violations).toEqual([
      { t: T0 + 5400, method: "POST", url: INVOICE_URL, reason: "domain write with no approval event" },
    ]);
    expect(evaluateSession(declined.expired, "after-approval").violations).toEqual([
      { t: T0 + 900000, method: "POST", url: INVOICE_URL, reason: "domain write with no approval event" },
    ]);
  });

  test("a navigation-only session passes no-writes and fails once a domain write is added", () => {
    expect(evaluateSession(navigationOnly, "no-writes").violations).toEqual([]);
    const withWrite = { ...navigationOnly, entries: [...navigationOnly.entries, entry("POST", "/api/v1/ap/invoices", "{}", T0 + 4000)] };
    expect(evaluateSession(withWrite, "no-writes").violations).toMatchObject([{ method: "POST", url: INVOICE_URL, t: T0 + 4000 }]);
  });

  test("an approval event never excuses a write in no-writes mode", () => {
    const verdict = evaluateSession(session([entry("POST", "/api/v1/ap/invoices", "{}", T0 + 2000)], [{ type: "approve", t: T0 + 1000 }]), "no-writes");
    expect(verdict.violations).toHaveLength(1);
  });

  test("an APPROVED decision issued before the approve event fails", () => {
    const decide = entry("POST", "/api/v1/agents/approvals/a-1:decide", JSON.stringify({ decision: "APPROVED" }), T0 + 1000);
    const verdict = evaluateSession(session([decide], [{ type: "approve", t: T0 + 2000 }]), "after-approval");
    expect(verdict.violations).toHaveLength(1);
  });

  test("an empty session passes in both modes", () => {
    expect(evaluateSession(session([]), "no-writes").violations).toEqual([]);
    expect(evaluateSession(session([]), "after-approval").violations).toEqual([]);
  });
});

test.describe("decline rule", () => {
  const routes = ["/api/v1/agents/approvals/a-1:decide", "/api/v1/agents/runs/r-1/actions/x-1/decision"];
  for (const route of routes) {
    test(`${route} is allowed only for a REJECTED decision`, () => {
      expect(classify(entry("POST", route, JSON.stringify({ decision: "REJECTED" })))).toBe("allowed");
      for (const body of [JSON.stringify({ decision: "APPROVED" }), "", null, undefined, "not json", JSON.stringify({ decision: 1 }), JSON.stringify({ decision: "rejected" }), JSON.stringify({ decision: { v: "REJECTED" } }), "null"]) {
        expect(classify(entry("POST", route, body)), String(body)).toBe("write");
      }
    });
  }
});

test.describe("domain write classification", () => {
  test("only non-GET requests under /api/v1/ are domain writes", () => {
    expect(isDomainWrite(entry("GET", "/api/v1/ap/invoices"))).toBe(false);
    expect(isDomainWrite(entry("POST", "/assets/app.js"))).toBe(false);
    expect(isDomainWrite(entry("POST", "/bff/session/heartbeat"))).toBe(false);
    expect(isDomainWrite(entry("POST", "/api/v1/ap/invoices"))).toBe(true);
    expect(isDomainWrite(entry("post", "/api/v1/ap/invoices"))).toBe(true);
    expect(isDomainWrite(entry("HEAD", "/api/v1/ap/invoices"))).toBe(true);
    expect(isDomainWrite(entry("OPTIONS", "/api/v1/ap/invoices"))).toBe(true);
    expect(isDomainWrite({ t: T0, method: "POST", url: "/api/v1/ap/invoices" })).toBe(true);
  });

  test("an unparseable or empty url is a violation, never skipped", () => {
    for (const url of ["", "   ", "http://"]) {
      const verdict = evaluateSession(session([{ t: T0, method: "GET", url }]), "no-writes");
      expect(verdict.violations, url).toMatchObject([{ reason: "unparseable request url" }]);
    }
  });
});

test.describe("session validation", () => {
  const bad: [string, unknown][] = [
    ["not an object", null],
    ["missing events", { entries: [] }],
    ["string timestamp", session([{ t: "1" as unknown as number, method: "GET", url: INVOICE_URL }])],
    ["NaN timestamp", session([{ t: Number.NaN, method: "GET", url: INVOICE_URL }])],
    ["non-string method", session([{ t: T0, method: 1 as unknown as string, url: INVOICE_URL }])],
    ["unknown event type", session([], [{ type: "decline", t: T0 } as never])],
    ["string event time", session([], [{ type: "approve", t: "1" as unknown as number }])],
    ["future event time", session([], [{ type: "approve", t: Date.now() + 3_600_000 }])],
  ];
  for (const [name, raw] of bad) {
    test(`rejects ${name}`, () => {
      expect(() => parseSession(raw)).toThrow(/writeGuard:/);
    });
  }
});

const ORIGIN = "https://guard.test";
const APPROVE_PAGE = `<button id="approve" onclick="fetch('/api/v1/agents/approvals/a-1:decide',{method:'POST',body:JSON.stringify({decision:'APPROVED'})}).then(()=>fetch('/api/v1/ap/invoices',{method:'POST',body:'{}'}))">Approve</button>`;

test.describe("writeGuard fixture on a live page", () => {
  test.beforeEach(async ({ page }) => {
    await page.route(`${ORIGIN}/**`, (route) => {
      const { pathname } = new URL(route.request().url());
      return pathname === "/"
        ? route.fulfill({ contentType: "text/html", body: APPROVE_PAGE })
        : route.fulfill({ contentType: "application/json", body: "{}" });
    });
    await page.goto(`${ORIGIN}/`);
  });

  test("expectNoWrites passes for GETs and allowlisted agent POSTs and throws on a domain write", async ({ page, writeGuard }) => {
    await page.evaluate(async () => {
      await fetch("/api/v1/ap/invoices");
      await fetch("/api/v1/agents/copilot/conversations", { method: "POST", body: "{}" });
    });
    writeGuard.expectNoWrites();
    await page.evaluate(() => fetch("/api/v1/ap/invoices", { method: "POST", body: "{}" }));
    expect(() => writeGuard.expectNoWrites()).toThrow(/POST https:\/\/guard\.test\/api\/v1\/ap\/invoices at \d{4}-/);
    expect(writeGuard.session().entries.map((e) => e.method)).toContain("POST");
  });

  test("expectWritesOnlyAfterApproval permits the decision and commit that follow the click", async ({ page, writeGuard }) => {
    const before = Date.now();
    await writeGuard.expectWritesOnlyAfterApproval(page.getByRole("button", { name: "Approve" }));
    expect(writeGuard.approvedAt).toBeGreaterThanOrEqual(before);
    expect(writeGuard.session().entries.filter((e) => e.method === "POST")).toHaveLength(2);
  });

  test("expectWritesOnlyAfterApproval throws when a domain write precedes the click", async ({ page, writeGuard }) => {
    await page.evaluate(() => fetch("/api/v1/ap/invoices", { method: "POST", body: "{}" }));
    await expect(writeGuard.expectWritesOnlyAfterApproval(page.getByRole("button", { name: "Approve" }))).rejects.toThrow(/domain write before the approval event/);
  });

  test("expectWritesOnlyAfterApproval counts a write fired while the button was still not clickable as before the approval", async ({ page, writeGuard }) => {
    const latePage = `<button id="approve" disabled>Approve</button><script>
      setTimeout(() => fetch('/api/v1/ap/invoices', { method: 'POST', body: '{}' }), 100);
      setTimeout(() => { document.getElementById('approve').disabled = false; }, 600);
    </script>`;
    await page.route(`${ORIGIN}/late`, (route) => route.fulfill({ contentType: "text/html", body: latePage }));
    await page.goto(`${ORIGIN}/late`);
    await expect(writeGuard.expectWritesOnlyAfterApproval(page.getByRole("button", { name: "Approve" }))).rejects.toThrow(/domain write before the approval event/);
  });
});
