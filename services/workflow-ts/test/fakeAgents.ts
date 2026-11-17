import { decodeClaims, problem, type ServiceClaims } from "./fakePayables.js";
import type { AgentStepOutcome } from "../src/agentRunActivities.js";

type Step = "begin" | "plan" | "resolve" | "commit" | "abort";

/**
 * The agents service's run steps as the workflow sees them (the real logic and its tests are in services/agents-ts).
 * State is held here, outside any worker, so a test can replace the worker mid-run and the run carries on.
 */
export class FakeAgents {
  readonly calls: Record<Step, number> = { begin: 0, plan: 0, resolve: 0, commit: 0, abort: 0 };
  readonly tokens: ServiceClaims[] = [];
  /** The stored decision: set by the test as the approver's decision would be. */
  decision: { decision: "APPROVED" | "REJECTED"; reason?: string } | undefined;
  /** What `plan` answers: by default the proposal waits for approval until `expiresAtMs`. */
  planOutcome: (() => AgentStepOutcome) | undefined;
  refuse: { step: Step; status: number } | undefined;
  /** Steps that answer 503 this many times first (an agents pod or its database briefly unavailable). */
  transient: Partial<Record<Step, number>> = {};
  /** The approver's session had expired: the next approved `resolve` opens a second approval request instead of committing. */
  reapproveOnce = false;
  private actionNo = 1;
  commitsPosted = 0;
  aborted: string | undefined;
  expiresAtMs = 0;
  private done: AgentStepOutcome | undefined;

  readonly fetch: typeof fetch = async (input, init) => {
    const claims = decodeClaims(new Headers(init?.headers).get("X-Depot-Service-Token"));
    if (!claims || claims.iss !== "workflow-ts" || claims.aud !== "agents" || claims.duties.join() !== "agents.run.workflow" || !claims.act || claims.le !== undefined) {
      return problem(401, "missing or wrong service token");
    }
    this.tokens.push(claims);
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const [, , step] = /^\/api\/v1\/agents\/runs\/([^/]+)\/steps\/(begin|plan|resolve|commit|abort)$/.exec(url.pathname) ?? [];
    if (!step) return problem(404, "unknown route");
    const name = step as Step;
    this.calls[name] += 1;
    if (this.refuse?.step === name) return problem(this.refuse.status, `refused ${name}`);
    if ((this.transient[name] ?? 0) > 0) {
      this.transient[name] = (this.transient[name] ?? 0) - 1;
      return problem(503, `unavailable ${name}`);
    }
    const body = init?.body ? (JSON.parse(String(init.body)) as { expire?: boolean; reason?: string }) : {};
    return new Response(JSON.stringify(this.apply(name, body)), { status: 200, headers: { "Content-Type": "application/json" } });
  };

  private apply(step: Step, body: { expire?: boolean; reason?: string }): AgentStepOutcome {
    if (this.done) return this.done;
    switch (step) {
      case "begin":
        return { next: "plan" };
      case "plan":
        return this.planOutcome ? this.finish(this.planOutcome()) : { next: "await_approval", actionId: `action-${this.actionNo}`, expiresAtMs: this.expiresAtMs };
      case "resolve":
        if (this.decision?.decision === "APPROVED" && this.reapproveOnce) {
          this.reapproveOnce = false;
          this.decision = undefined;
          this.actionNo += 1;
          return { next: "await_approval", actionId: `action-${this.actionNo}`, expiresAtMs: this.expiresAtMs };
        }
        if (this.decision?.decision === "APPROVED") return { next: "commit" };
        if (this.decision) return this.finish({ next: "done", status: "REJECTED", reason: this.decision.reason ?? "approver rejected" });
        if (body.expire) {
          this.decision = { decision: "REJECTED", reason: "EXPIRED" };
          return this.finish({ next: "done", status: "REJECTED", code: "AGT-0208", reason: "EXPIRED" });
        }
        return { next: "await_approval", actionId: `action-${this.actionNo}`, expiresAtMs: this.expiresAtMs };
      case "commit":
        this.commitsPosted += 1;
        return this.finish({ next: "done", status: "COMMITTED" });
      default:
        this.aborted = body.reason;
        return this.finish({ next: "done", status: "FAILED", reason: body.reason ?? "aborted" });
    }
  }

  private finish(out: AgentStepOutcome): AgentStepOutcome {
    if (out.next === "done") this.done = out;
    return out;
  }
}
