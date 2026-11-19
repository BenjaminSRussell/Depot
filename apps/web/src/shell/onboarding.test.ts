import { describe, expect, it } from "vitest";
import authz from "../../../../policies/authz/data.json";
import { copy } from "./copy";
import { canPersist, onboardingKey, readOnboarding, resetOnboarding, selectSteps, shouldAutoStart, writeOnboarding } from "./onboarding";
import { noPreferenceStorage } from "./preferenceStorage";
import { plainRules } from "./plainLanguageRules";
import { memoryStorage } from "./testing/onboardingFixtures";

// @ts-expect-error TS2307: apps/web does not install node types; vitest runs on node
const { readFileSync } = (await import("node:fs")) as { readFileSync: (file: string, encoding: "utf8") => string };
const tourCss = readFileSync("src/shell/OnboardingTour.module.css", "utf8");

const ALL = { copilotDuty: true, aiEnabled: true, showsFeed: true };
const input = (storage = memoryStorage(), overrides = {}) => ({ identity: "t.u1", storage, pathname: "/", aiStatus: "enabled", demo: false, ...overrides });

describe("onboarding state", () => {
  it("starts as none, then records skipped or completed per user, and resets", () => {
    const storage = memoryStorage();
    expect(readOnboarding("t.u1", storage)).toBe("none");
    expect(writeOnboarding("t.u1", "skipped", storage)).toBe(true);
    expect(readOnboarding("t.u1", storage)).toBe("skipped");
    expect(readOnboarding("t.u2", storage)).toBe("none");
    writeOnboarding("t.u1", "completed", storage);
    expect(readOnboarding("t.u1", storage)).toBe("completed");
    resetOnboarding("t.u1", storage);
    expect(readOnboarding("t.u1", storage)).toBe("none");
    expect(storage.entries.has(onboardingKey("t.u1"))).toBe(false);
  });

  it.each(["", "true", "1", "[]", '{"a":1}', "NaN", "Skipped"])("reads the stored value %j as none", (value) => {
    expect(readOnboarding("t.u1", memoryStorage({ [onboardingKey("t.u1")]: value }))).toBe("none");
  });

  it("cannot persist without an identity or with storage that refuses writes", () => {
    expect(readOnboarding("", memoryStorage())).toBe("none");
    expect(writeOnboarding("", "skipped", memoryStorage())).toBe(false);
    expect(canPersist("", memoryStorage())).toBe(false);
    expect(canPersist("t.u1", noPreferenceStorage)).toBe(false);
    expect(writeOnboarding("t.u1", "skipped", noPreferenceStorage)).toBe(false);
    const storage = memoryStorage();
    expect(canPersist("t.u1", storage)).toBe(true);
    expect(storage.entries.size).toBe(0);
  });
});

describe("auto-start rule", () => {
  it("starts for a first visit to the home page", () => {
    expect(shouldAutoStart(input())).toBe(true);
  });

  it.each([
    ["while availability is checking", { aiStatus: "checking" }],
    ["off the home page", { pathname: "/ledger" }],
    ["in demo mode", { demo: true }],
    ["without an identity", { identity: "" }],
    ["when storage cannot persist", { storage: noPreferenceStorage }],
  ])("does not start %s", (_name, overrides) => {
    expect(shouldAutoStart(input(memoryStorage(), overrides))).toBe(false);
  });

  it.each(["skipped", "completed"] as const)("does not start after %s", (state) => {
    const storage = memoryStorage();
    writeOnboarding("t.u1", state, storage);
    expect(shouldAutoStart(input(storage))).toBe(false);
  });
});

describe("step selection", () => {
  it("lists every step in order for a user with the duty, AI and the feed", () => {
    expect(selectSteps(ALL)).toEqual(["prompt", "feed", "commandBar", "approvals", "classicNav"]);
  });

  it("drops the AI steps without the copilot duty or when AI is not enabled, keeping classic navigation", () => {
    expect(selectSteps({ ...ALL, copilotDuty: false })).toEqual(["feed", "commandBar", "classicNav"]);
    expect(selectSteps({ ...ALL, aiEnabled: false })).toEqual(["feed", "commandBar", "classicNav"]);
  });

  it("drops the feed step when the home shows no work summary", () => {
    expect(selectSteps({ ...ALL, showsFeed: false })).toEqual(["prompt", "commandBar", "approvals", "classicNav"]);
  });

  it("never mentions the assistant in a step that stays when AI is off", () => {
    for (const id of selectSteps({ copilotDuty: false, aiEnabled: false, showsFeed: false })) {
      const { title, body } = copy.onboarding.steps[id];
      expect(`${title} ${body}`).not.toMatch(/assistant/i);
    }
  });
});

describe("onboarding copy", () => {
  it("states the approvals limits", () => {
    const { body } = copy.onboarding.steps.approvals;
    expect(body).toContain("can't do more than your own permissions allow");
    expect(body).toContain("posts or touches money needs your approval");
  });

  it("passes the plain-language rules", () => {
    const { steps, ...rest } = copy.onboarding;
    const strings = [
      ...Object.values(steps).flatMap(({ title, body }) => [title, body]),
      rest.stepOf(2, 5),
      rest.announceStep(2, 5, steps.prompt.title),
      rest.next,
      rest.back,
      rest.skip,
      rest.finish,
      copy.help.takeTour,
    ];
    for (const [name, pattern] of Object.entries(plainRules(Object.keys(authz.duties)))) {
      expect(strings.filter((text) => pattern.test(text)), name).toEqual([]);
    }
  });
});

describe("tour motion", () => {
  it("animates only when the user has no reduced-motion preference", () => {
    const motion = tourCss.indexOf("prefers-reduced-motion: no-preference");
    expect(motion).toBeGreaterThan(-1);
    expect(tourCss.indexOf("animation")).toBeGreaterThan(motion);
    expect(tourCss.indexOf("transition")).toBe(-1);
  });
});
