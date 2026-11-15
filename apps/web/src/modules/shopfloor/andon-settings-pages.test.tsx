import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { installLookupStub, lookupName } from "../purchasing/lookupStub";
import type { AndonSettings, EdgeCertificate, EdgeNode } from "./api/client";
import { validateThresholds } from "./AndonSettingsPage";
import shopfloor from "./module";

const SITE = "0195f0a0-0000-7000-8000-000000000a01";
const EDGE = "0195f0a0-0000-7000-8000-000000000051";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  installLookupStub();
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      if (reply.status === 204) return new Response(null, { status: 204 });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: readonly string[]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const fill = (label: string | RegExp, value: string, form?: string) =>
  fireEvent.change((form ? within(screen.getByRole("form", { name: form })) : screen).getByLabelText(label), { target: { value } });

const settings = (over: Partial<AndonSettings> = {}): AndonSettings => ({
  downtimeMinutes: 10,
  clearMinutes: 5,
  scrapPercent: 5,
  scrapMinCount: 10,
  default: true,
  sites: [],
  ...over,
});

describe("andon settings", () => {
  it("validates like the service", () => {
    expect(validateThresholds({ downtimeMinutes: "3", clearMinutes: "0", scrapPercent: "2", scrapMinCount: "20" }).input).toEqual({ downtimeMinutes: 3, clearMinutes: 0, scrapPercent: 2, scrapMinCount: 20 });
    const { errors } = validateThresholds({ downtimeMinutes: "0", clearMinutes: "121", scrapPercent: "101", scrapMinCount: "x" });
    expect(Object.keys(errors).sort()).toEqual(["clearMinutes", "downtimeMinutes", "scrapMinCount", "scrapPercent"]);
  });

  it("shows the standard values until the tenant sets its own, and saves new ones", async () => {
    let current = settings();
    handler = (url, init) => {
      if (url.pathname === "/api/v1/shopfloor/andon-settings" && init.method === "PUT") {
        current = settings({ downtimeMinutes: 3, clearMinutes: 1, scrapPercent: 2, scrapMinCount: 20, default: false, updatedBy: "u-1", updatedAt: "2026-11-02T09:00:00Z" });
        return json(200, current);
      }
      if (url.pathname === "/api/v1/shopfloor/andon-settings") return json(200, current);
      return undefined;
    };
    const { container } = renderAt("/shopfloor/andon-settings", ["shopfloor.view", "shopfloor.setup.maintain"]);
    expect(await screen.findByText(/the standard values apply/)).toBeInTheDocument();
    expect((within(screen.getByRole("form", { name: "Andon thresholds" })).getByLabelText("Minutes a machine may stay down") as HTMLInputElement).value).toBe("10");
    expect(pageViolations(container)).toEqual([]);
    fill("Minutes a machine may stay down", "0", "Andon thresholds");
    fireEvent.click(screen.getByRole("button", { name: "Save thresholds" }));
    expect((await screen.findAllByText("Enter a whole number from 1 to 240.")).length).toBeGreaterThan(0);
    fill("Minutes a machine may stay down", "3", "Andon thresholds");
    fill("Minutes a condition must stay clear", "1", "Andon thresholds");
    fill("Scrap share that raises an alert (%)", "2", "Andon thresholds");
    fill("Fewest pieces a bucket must have", "20", "Andon thresholds");
    fireEvent.click(screen.getByRole("button", { name: "Save thresholds" }));
    expect(await screen.findByText("Thresholds saved")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ downtimeMinutes: 3, clearMinutes: 1, scrapPercent: 2, scrapMinCount: 20 });
    expect(container.textContent).not.toContain("u-1");
  });

  it("lists the sites with values of their own, sets one and removes one", async () => {
    let sites = [{ siteId: SITE, downtimeMinutes: 30, clearMinutes: 2, scrapPercent: 8, scrapMinCount: 50, updatedBy: "u-1", updatedAt: "2026-11-02T09:00:00Z" }];
    handler = (url, init) => {
      if (url.pathname === `/api/v1/shopfloor/andon-settings/sites/${SITE}` && init.method === "DELETE") {
        sites = [];
        return json(204, null);
      }
      if (url.pathname.startsWith("/api/v1/shopfloor/andon-settings/sites/") && init.method === "PUT") return json(200, { siteId: SITE, ...(init.body ? JSON.parse(String(init.body)) : {}), updatedBy: "u-1", updatedAt: "2026-11-02T09:00:00Z" });
      if (url.pathname === "/api/v1/shopfloor/andon-settings") return json(200, settings({ default: false, sites }));
      return undefined;
    };
    const { container } = renderAt("/shopfloor/andon-settings", ["shopfloor.view", "shopfloor.setup.maintain"]);
    const table = await findTable({ name: "Sites with their own andon values" });
    await waitFor(() => expect(table).toHaveTextContent(lookupName("Site", SITE)));
    expect(table).toHaveTextContent("30");
    expect(container.textContent).not.toContain(SITE);
    fireEvent.click(within(table).getByRole("button", { name: "Use the values for every site" }));
    expect(await screen.findByText("No site has values of its own")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Save values for the site" }));
    expect((await screen.findAllByText("Choose the site.")).length).toBeGreaterThan(0);
    fill("Site", SITE, "Andon thresholds of a site");
    fill("Minutes a machine may stay down", "30", "Andon thresholds of a site");
    fireEvent.click(screen.getByRole("button", { name: "Save values for the site" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.pathname === `/api/v1/shopfloor/andon-settings/sites/${SITE}`)).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ downtimeMinutes: 30, clearMinutes: 5, scrapPercent: 5, scrapMinCount: 10 });
  });

  it("is read only without the setup duty", async () => {
    handler = (url) => (url.pathname === "/api/v1/shopfloor/andon-settings" ? json(200, settings()) : undefined);
    renderAt("/shopfloor/andon-settings", ["shopfloor.view"]);
    expect(await screen.findByText(/needs the shop floor setup duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save thresholds" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Set values for one site" })).not.toBeInTheDocument();
  });
});

const node: EdgeNode = {
  id: EDGE,
  name: "Press line gateway",
  siteId: null,
  platform: "linux",
  ring: "all",
  connectorSubject: `edge:${EDGE}`,
  connectorVersion: "1.4.0",
  connectorSerial: "0A1B",
  certExpiresAt: "2027-02-01T00:00:00Z",
  lastHeartbeatAt: "2026-11-02T08:59:00Z",
  bufferFillPct: 3,
  appliedConfigVersion: 1,
  status: "online",
  active: true,
  version: 2,
  machines: [],
} as unknown as EdgeNode;

const certs: EdgeCertificate[] = [
  { serial: "0A1B2C3D4E5F6071", subject: `edge:${EDGE}`, reason: "renew", notBefore: "2026-10-01T00:00:00Z", notAfter: "2027-02-01T00:00:00Z", issuedAt: "2026-10-01T00:00:00Z", fingerprint: "ab".repeat(32), current: true },
  { serial: "0000000000000001", subject: `edge:${EDGE}`, reason: "enroll", notBefore: "2026-06-01T00:00:00Z", notAfter: "2026-12-01T00:00:00Z", issuedAt: "2026-06-01T00:00:00Z", fingerprint: "cd".repeat(32), current: false, revokedAt: "2026-10-01T00:00:00Z", revokedBy: "u-1", revokeReason: "replaced at renewal" },
];

describe("edge certificate history", () => {
  function serve(list: EdgeCertificate[]) {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":revoke")) {
        list = list.map((c) => (url.pathname.includes(c.serial) ? { ...c, revokedAt: "2026-11-02T10:00:00Z", revokedBy: "u-1", revokeReason: "lost" } : c));
        return json(200, list[0]);
      }
      if (url.pathname === `/api/v1/shopfloor/edge-nodes/${EDGE}/certificates`) return json(200, { items: list });
      if (url.pathname === `/api/v1/shopfloor/edge-nodes/${EDGE}`) return json(200, node);
      if (url.pathname === `/api/v1/shopfloor/edge-nodes/${EDGE}/config-versions`) return json(200, { items: [] });
      return undefined;
    };
  }

  it("lists every certificate with its status and who revoked it", async () => {
    serve(certs);
    const { container } = renderAt(`/shopfloor/edge-nodes/${EDGE}`, ["shopfloor.view"]);
    const table = await findTable({ name: "Certificates" });
    await waitFor(() => expect(table).toHaveTextContent("0A1B2C3D4E5F6071"));
    expect(table).toHaveTextContent("Current");
    expect(table).toHaveTextContent("Revoked");
    expect(table).toHaveTextContent("replaced at renewal");
    expect(table).toHaveTextContent("abababababab");
    expect(within(table).queryByRole("button", { name: /Revoke certificate/ })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("revokes the current certificate with a reason and warns that the node is unbound", async () => {
    serve(certs);
    renderAt(`/shopfloor/edge-nodes/${EDGE}`, ["shopfloor.view", "shopfloor.edge.configure"]);
    const table = await findTable({ name: "Certificates" });
    await waitFor(() => expect(table).toHaveTextContent("0A1B2C3D4E5F6071"));
    fireEvent.click(within(table).getByRole("button", { name: /Revoke certificate 0A1B2C3D4E5F6071/ }));
    expect(await screen.findByText(/Revoking it unbinds the node/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Revoke certificate" }));
    expect(await screen.findByText("Enter why the certificate is revoked, up to 500 characters.")).toBeInTheDocument();
    fill("Reason", "lost");
    fireEvent.click(screen.getByRole("button", { name: "Revoke certificate" }));
    expect(await screen.findByText(/the node is unbound|The node is unbound/i)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/shopfloor/edge-nodes/${EDGE}/certificates/0A1B2C3D4E5F6071:revoke`);
    expect(post?.body).toEqual({ reason: "lost" });
  });
});
