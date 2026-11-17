import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Gateway } from "../src/gateway.ts";
import { SandboxCarrier } from "../src/sandbox.ts";
import { createHttpServer } from "../src/server.ts";
import { createVerifier, parsePublicKeys } from "../src/serviceToken.ts";
import { NOW_MS, SMALL, testSigner, US_FROM, US_TO } from "./helpers.ts";

// The default request log pseudonymizes the acting person under the EU data boundary overlay (#605).
const KEY_B64 = Buffer.from(Array.from({ length: 32 }, (_, i) => i)).toString("base64");

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("the default log line", () => {
  it("never carries the raw actor id under the eu-data-boundary overlay", async () => {
    vi.stubEnv("DEPOT_CLOUD_PROFILE", "commercial+eu-data-boundary");
    vi.stubEnv("FIELDCRYPT_KEYS", `k1=${KEY_B64}`);
    vi.stubEnv("FIELDCRYPT_CURRENT_KEY_ID", "k1");
    const written: string[] = [];
    const write = process.stdout.write.bind(process.stdout);
    vi.spyOn(process.stdout, "write").mockImplementation(((chunk: string | Uint8Array, ...rest: unknown[]) => {
      if (typeof chunk === "string" && chunk.includes("carrier label")) {
        written.push(chunk);
        return true;
      }
      return (write as (...a: unknown[]) => boolean)(chunk, ...rest);
    }) as typeof process.stdout.write);
    const signer = testSigner("fulfillment", () => NOW_MS);
    const server = createHttpServer({
      gateway: new Gateway([new SandboxCarrier({ now: () => NOW_MS })]),
      verifier: createVerifier({ keys: parsePublicKeys(`fulfillment=${signer.publicKeyBase64}`), now: () => NOW_MS }),
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const res = await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/v1/carrier/labels`, {
        method: "POST",
        headers: { "x-depot-service-token": signer.mint(), "idempotency-key": "k-log-1", "content-type": "application/json" },
        body: JSON.stringify({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL], carrier: "sandbox", service: "GROUND", reference: "SH-1" }),
      });
      expect(res.status).toBe(201);
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
    expect(written).toHaveLength(1);
    expect(written[0]).not.toContain("0190a5f2-7b1c-7d3e-8a4f-0000000000aa");
    expect(JSON.parse(written[0]!).actor).toMatch(/^pn_[0-9a-f]{16}$/);
  });
});
