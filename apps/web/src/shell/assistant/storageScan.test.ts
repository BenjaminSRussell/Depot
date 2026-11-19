import { describe, expect, it } from "vitest";

const BROWSER_STORES = /\b(localStorage|sessionStorage|indexedDB|caches|CacheStorage)\b/g;
const PREFERENCE_STORAGE_IMPORT = /from\s+["'][^"']*preferenceStorage["']/;
const SERVICE_WORKER = /\bserviceWorker\b/;
const WRAPPER = "shell/assistant/assistantStorage.ts";

function findStorageViolations(source: string, file: string): string[] {
  const stores = [...source.matchAll(BROWSER_STORES)].map((m) => `${file}: direct use of ${m[1]}`);
  const wrapper = !file.endsWith(WRAPPER) && PREFERENCE_STORAGE_IMPORT.test(source) ? [`${file}: preferenceStorage used outside the assistant wrapper`] : [];
  return [...stores, ...wrapper];
}

const assistantSources = import.meta.glob<string>(
  ["/src/modules/copilot/**/*.{ts,tsx}", "/src/shell/assistant/**/*.{ts,tsx}", "/src/shell/home/**/*.{ts,tsx}", "!**/*.test.{ts,tsx}"],
  { query: "?raw", import: "default", eager: true },
);
const allSources = import.meta.glob<string>(["/src/**/*.{ts,tsx}", "!**/*.test.{ts,tsx}"], { query: "?raw", import: "default", eager: true });
const publicFiles = import.meta.glob<string>(["/public/**/*"], { query: "?raw", import: "default", eager: true });

const inDirectory = (directory: string) => Object.keys(assistantSources).filter((path) => path.includes(directory));

describe("assistant storage scan", () => {
  it("reads files from each assistant directory, so a renamed directory cannot pass silently", () => {
    expect(inDirectory("modules/copilot/").length).toBeGreaterThan(0);
    expect(inDirectory("shell/assistant/").length).toBeGreaterThan(0);
    expect(inDirectory("shell/home/").length).toBeGreaterThan(0);
  });

  it("scans the answer component and the conversation hook", () => {
    expect(inDirectory("modules/copilot/AskAnswer.tsx")).toHaveLength(1);
    expect(inDirectory("modules/copilot/useCopilotConversation.ts")).toHaveLength(1);
  });

  it("finds no direct browser storage write in the assistant surfaces", () => {
    const violations = Object.entries(assistantSources).flatMap(([file, source]) => findStorageViolations(source, file));
    expect(violations).toEqual([]);
  });

  it.each([
    ["localStorage", 'window.localStorage.setItem("k", answer);'],
    ["sessionStorage", 'sessionStorage.setItem("k", answer);'],
    ["indexedDB", 'indexedDB.open("answers");'],
    ["caches", 'const c = await caches.open("answers");'],
    ["CacheStorage", "let c: CacheStorage;"],
  ])("fails a source that uses %s directly", (_name, source) => {
    expect(findStorageViolations(source, "modules/copilot/x.ts")).not.toEqual([]);
  });

  it("fails a source that imports preferenceStorage outside the wrapper and passes the wrapper", () => {
    const source = 'import { localPreferenceStorage } from "../preferenceStorage";';
    expect(findStorageViolations(source, "shell/home/x.ts")).not.toEqual([]);
    expect(findStorageViolations(source, WRAPPER)).toEqual([]);
  });

  it("passes a source that keeps text in memory", () => {
    expect(findStorageViolations('const [answer, setAnswer] = useState("");', "modules/copilot/x.tsx")).toEqual([]);
  });

  it("has no service worker, so no assistant route can be cached by one", () => {
    expect(Object.keys(allSources).length).toBeGreaterThan(0);
    const registered = Object.entries(allSources).filter(([, source]) => SERVICE_WORKER.test(source)).map(([file]) => file);
    const scripts = Object.keys(publicFiles).filter((file) => /(^|\/)(sw|service-worker|workbox[^/]*)\.[jt]s$/.test(file));
    expect([...registered, ...scripts]).toEqual([]);
  });
});
