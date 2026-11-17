import { afterEach, describe, expect, it, vi } from "vitest";
import { signIn, startRig, type Rig } from "./helpers.ts";

// The default request log pseudonymizes the signed-in person under the EU data boundary overlay (#605).
const KEY_B64 = Buffer.from(Array.from({ length: 32 }, (_, i) => i)).toString("base64");
let rig: Rig;

afterEach(async () => {
  await rig.close();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("the default log line", () => {
  it("carries a pseudonym, never the raw user id, under the eu-data-boundary overlay", async () => {
    vi.stubEnv("DEPOT_CLOUD_PROFILE", "commercial+eu-data-boundary");
    vi.stubEnv("FIELDCRYPT_KEYS", `k1=${KEY_B64}`);
    vi.stubEnv("FIELDCRYPT_CURRENT_KEY_ID", "k1");
    const lines: string[] = [];
    const write = process.stdout.write.bind(process.stdout);
    vi.spyOn(process.stdout, "write").mockImplementation(((chunk: string | Uint8Array, ...rest: unknown[]) => {
      if (typeof chunk === "string" && chunk.includes('"msg":"sign-in"')) {
        lines.push(chunk);
        return true;
      }
      return (write as (...a: unknown[]) => boolean)(chunk, ...rest);
    }) as typeof process.stdout.write);
    rig = await startRig({ defaultLog: true });
    await signIn(rig);
    expect(lines).toHaveLength(1);
    const entry = JSON.parse(lines[0]!) as Record<string, string>;
    expect(entry.userId).toMatch(/^pn_[0-9a-f]{16}$/);
    expect(lines[0]).not.toContain("user-1");
  });
});
