import { describe, expect, it } from "vitest";
import { devIdentityHeaders, devProxy, stripApi } from "./devProxy";

function match(proxy: ReturnType<typeof devProxy>, url: string) {
  const key = Object.keys(proxy).find((k) => (k.startsWith("^") ? new RegExp(k).test(url) : url.startsWith(k)));
  return key ? proxy[key] : undefined;
}

describe("devProxy", () => {
  it("strips only the leading /api prefix, keeping the query", () => {
    expect(stripApi("/api/v1/ledger/ledgers")).toBe("/v1/ledger/ledgers");
    expect(stripApi("/api/v1/ledger/x?a=1")).toBe("/v1/ledger/x?a=1");
    expect(stripApi("/v1/api/x")).toBe("/v1/api/x");
  });

  it("sends everything to localhost:8080 by default, rewriting /api away and adding identity headers", () => {
    const hit = match(devProxy({}), "/api/v1/ledger/x");
    expect(hit?.target).toBe("http://localhost:8080");
    expect((hit?.rewrite as (p: string) => string)("/api/v1/ledger/x")).toBe("/v1/ledger/x");
    expect(hit?.headers).toMatchObject({ "X-Depot-Tenant": expect.any(String), "X-Depot-User": "dev", "X-Depot-Duties": "" });
  });

  it("honours DEPOT_SVC_DEFAULT_URL and does not turn it into a service route", () => {
    const proxy = devProxy({ DEPOT_SVC_DEFAULT_URL: "http://localhost:9000" });
    expect(Object.keys(proxy)).toEqual(["/api"]);
    expect(match(proxy, "/api/v1/default/x")?.target).toBe("http://localhost:9000");
  });

  it("routes overrides ahead of the default and never confuses ap with ap-invoices", () => {
    const proxy = devProxy({
      DEPOT_SVC_LEDGER_URL: "http://localhost:8081",
      DEPOT_SVC_AP_URL: "http://localhost:8082",
      DEPOT_SVC_AP_INVOICES_URL: "http://localhost:8083",
    });
    expect(match(proxy, "/api/v1/ledger/x")?.target).toBe("http://localhost:8081");
    expect(match(proxy, "/api/v1/ledger?limit=1")?.target).toBe("http://localhost:8081");
    expect(match(proxy, "/api/v1/ap/x")?.target).toBe("http://localhost:8082");
    expect(match(proxy, "/api/v1/ap-invoices/x")?.target).toBe("http://localhost:8083");
    expect(match(proxy, "/api/v1/ledgerx/x")?.target).toBe("http://localhost:8080");
    expect(match(proxy, "/api/v1/fx/x")?.target).toBe("http://localhost:8080");
  });

  it("in OIDC mode sends every call to the gateway untouched, with no injected identity", () => {
    const proxy = devProxy({ DEPOT_OIDC_ISSUER: "http://localhost:8080/realms/depot", DEPOT_GATEWAY_URL: "http://localhost:8088", DEPOT_SVC_LEDGER_URL: "http://localhost:1" });
    expect(Object.keys(proxy)).toEqual(["/api"]);
    expect(proxy["/api"]?.target).toBe("http://localhost:8088");
    expect(proxy["/api"]?.headers).toBeUndefined();
    expect(proxy["/api"]?.rewrite).toBeUndefined();
  });

  it("lets env override the identity headers", () => {
    expect(devIdentityHeaders({ DEPOT_DEV_TENANT: "t", DEPOT_DEV_DUTIES: "a.b,c.d" })).toMatchObject({
      "X-Depot-Tenant": "t",
      "X-Depot-Duties": "a.b,c.d",
    });
  });
});
