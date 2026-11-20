import type { PreferenceStorage } from "./preferenceStorage";

export type StoredOnboarding = "skipped" | "completed";
export type OnboardingState = StoredOnboarding | "none";
export type StepId = "prompt" | "feed" | "commandBar" | "approvals" | "classicNav";

const PROBE_KEY = "depot.onboarding.probe";

export const onboardingKey = (identity: string) => `depot.onboarding.v1.${identity}`;

export function readOnboarding(identity: string, storage: PreferenceStorage): OnboardingState {
  if (identity === "") return "none";
  const stored = storage.read(onboardingKey(identity));
  return stored === "skipped" || stored === "completed" ? stored : "none";
}

export function writeOnboarding(identity: string, value: StoredOnboarding, storage: PreferenceStorage): boolean {
  return identity !== "" && storage.write(onboardingKey(identity), value);
}

export function resetOnboarding(identity: string, storage: PreferenceStorage): void {
  if (identity !== "") storage.remove(onboardingKey(identity));
}

export function canPersist(identity: string, storage: PreferenceStorage): boolean {
  if (identity === "" || !storage.write(PROBE_KEY, "1")) return false;
  storage.remove(PROBE_KEY);
  return true;
}

export interface AutoStartInput {
  identity: string;
  storage: PreferenceStorage;
  pathname: string;
  aiStatus: string;
  demo: boolean;
}

export function shouldAutoStart({ identity, storage, pathname, aiStatus, demo }: AutoStartInput): boolean {
  return (
    !demo &&
    pathname === "/" &&
    aiStatus !== "checking" &&
    readOnboarding(identity, storage) === "none" &&
    canPersist(identity, storage)
  );
}

export interface StepInput {
  copilotDuty: boolean;
  aiEnabled: boolean;
  showsFeed: boolean;
}

export function selectSteps({ copilotDuty, aiEnabled, showsFeed }: StepInput): StepId[] {
  const ai = copilotDuty && aiEnabled;
  const steps: (StepId | false)[] = [ai && "prompt", showsFeed && "feed", "commandBar", ai && "approvals", "classicNav"];
  return steps.filter((step): step is StepId => step !== false);
}
