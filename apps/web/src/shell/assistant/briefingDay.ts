import { createAssistantStore } from "./assistantStorage";

const store = createAssistantStore();
const claimed = new Set<string>();

export function localDay(at: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
}

export function claimDailyBriefing(identity: string, now: Date = new Date()): boolean {
  const day = localDay(now);
  const memoryKey = `${identity}|${day}`;
  if (claimed.has(memoryKey)) return false;
  claimed.add(memoryKey);
  if (store.read("briefingDay", identity).some((row) => row.day === day)) return false;
  store.write("briefingDay", identity, [{ day, at: now.toISOString() }]);
  return true;
}
