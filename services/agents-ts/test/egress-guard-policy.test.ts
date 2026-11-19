import { describe, expect, it, vi } from "vitest";
import { AgentError } from "../src/errors.js";
import { createEgressGuard } from "../src/runtime/egressGuard.js";

vi.mock("../src/agents/catalog.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../src/agents/catalog.js")>()),
  toolAnnotation: (tool: string) => ({ module: tool.split(".")[1] ?? "", declared: tool.endsWith(".p0") ? "P0" : "P1" }),
}));

const T1 = "11111111-1111-4111-8111-111111111111";

describe("egress guard policy", () => {
  const unreadable = createEgressGuard({
    policy: { check: async () => Promise.reject(new AgentError("AGT-0216", "the AI data policy could not be read")), invalidate: () => undefined },
  });

  it.each(["depot.ap.invoice.p0", "depot.ap.invoice.p1", "depot.ar.invoice.p0", "depot.ar.invoice.p1"])(
    "blocks %s with POLICY_UNREADABLE when the tenant policy cannot be read",
    async (tool) => {
      expect(await unreadable.admit({ tenantId: T1, tool, text: "{}" })).toMatchObject({ pass: false, failure: "POLICY_UNREADABLE", policyCell: "tenant", class: "P3" });
    },
  );
});
