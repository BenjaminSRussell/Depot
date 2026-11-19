import { onboardingKey, type OnboardingState } from "../onboarding";
import type { PreferenceStorage } from "../preferenceStorage";

export function memoryStorage(seed: Record<string, string> = {}): PreferenceStorage & { entries: Map<string, string> } {
  const entries = new Map(Object.entries(seed));
  return {
    entries,
    read: (key) => entries.get(key) ?? null,
    write: (key, value) => {
      entries.set(key, value);
      return true;
    },
    remove: (key) => void entries.delete(key),
  };
}

export function onboardingStorage(identity: string, state: OnboardingState = "completed") {
  return memoryStorage(state === "none" ? {} : { [onboardingKey(identity)]: state });
}

export const completedOnboardingStorage = (identity: string) => onboardingStorage(identity, "completed");
