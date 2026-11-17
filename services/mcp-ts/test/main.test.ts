import { describe, expect, it } from "vitest";
import { loadConfig } from "../src/main.js";

const base = { DEPOT_API_URL: "http://api.depot.svc:8080", DEPOT_OPENAPI_JSON: "/etc/depot/openapi.json" };

describe("loadConfig (12-factor startup config)", () => {
  it("applies the defaults for address and build", () => {
    expect(loadConfig(base)).toEqual({
      apiUrl: base.DEPOT_API_URL,
      openApiFile: base.DEPOT_OPENAPI_JSON,
      host: "0.0.0.0",
      port: 8080,
      build: "0.1.0",
      adminHost: "0.0.0.0",
      adminPort: 9090,
    });
  });

  it("reads HTTP_ADDR, ADMIN_ADDR and SERVICE_VERSION", () => {
    expect(loadConfig({ ...base, HTTP_ADDR: "127.0.0.1:9000", ADMIN_ADDR: "127.0.0.1:9191", SERVICE_VERSION: "abc123" })).toMatchObject({
      host: "127.0.0.1",
      port: 9000,
      build: "abc123",
      adminHost: "127.0.0.1",
      adminPort: 9191,
    });
  });

  it.each([
    ["a non-http API URL", { ...base, DEPOT_API_URL: "ftp://x" }, /DEPOT_API_URL/],
    ["a missing OpenAPI file", { DEPOT_API_URL: base.DEPOT_API_URL }, /DEPOT_OPENAPI_JSON/],
    ["an address without a port", { ...base, HTTP_ADDR: "localhost" }, /HTTP_ADDR/],
    ["a port out of range", { ...base, HTTP_ADDR: ":70000" }, /HTTP_ADDR/],
    ["an admin address without a port", { ...base, ADMIN_ADDR: "localhost" }, /ADMIN_ADDR/],
    ["the admin listener on the API port", { ...base, ADMIN_ADDR: ":8080" }, /different port/],
  ])("refuses %s at startup", (_name, env, message) => {
    expect(() => loadConfig(env)).toThrow(message);
  });
});
