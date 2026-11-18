import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

export const SHELL_SENTINELS = ["AP1", "AR1", "BU1", "WH1", "SR1", "PP1", "CT1", "TA1", "MD1", "AM1", "MT1", "AU1"];

const APP_PATHS = [
  ["apps/customer-portal/", "customer-portal"],
  ["apps/supplier-portal/", "supplier-portal"],
  ["apps/technician/", "technician"],
  ["apps/warehouse-mobile/", "device"],
  ["apps/web/src/modules/mobile-flows/", "device"],
];

const MODULE_PATH = /^apps\/web\/src\/modules\/([^/]+)\//;
const TEST_FILE = /\.test\.[cm]?[jt]sx?$/;
const RANGE = /^[\w./~^][\w./~^-]*\.\.[\w./~^][\w./~^-]*$/;

export function isRange(value) {
  return RANGE.test(value);
}

export function flattenTasks(json) {
  return Object.entries(json.personas).flatMap(([persona, { tasks }]) =>
    Object.entries(tasks).map(([id, [route, module]]) => ({ id, persona, route, module })),
  );
}

export function loadTasks() {
  return flattenTasks(JSON.parse(readFileSync(join(import.meta.dirname, "../src/shell/usabilityTasks.json"), "utf8")));
}

export function computeScope(changedFiles, tasks) {
  const scope = new Map();
  const add = (ids, what) => {
    for (const id of ids) if (!scope.has(id)) scope.set(id, `${id} is in scope (${what} changed)`);
  };
  const inModule = (module) => tasks.filter((t) => t.module === module).map((t) => t.id);
  for (const file of changedFiles.filter((f) => !TEST_FILE.test(f))) {
    const moduleDir = MODULE_PATH.exec(file);
    if (moduleDir) add(inModule(moduleDir[1]), `apps/web/src/modules/${moduleDir[1]}`);
    if (file.startsWith("apps/web/src/shell/")) add(SHELL_SENTINELS, "apps/web/src/shell");
    for (const [prefix, module] of APP_PATHS) if (file.startsWith(prefix)) add(inModule(module), prefix.replace(/\/$/, ""));
  }
  return scope;
}

export function changedFiles(range) {
  return execFileSync("git", ["diff", "--name-only", range, "--"], { encoding: "utf8" }).split("\n").filter(Boolean);
}

if (process.argv[1] === import.meta.filename) {
  const range = process.argv[2];
  if (!range || !isRange(range)) {
    console.error("usage: node scripts/usability-scope.mjs <from>..<to>");
    process.exit(2);
  }
  for (const reason of computeScope(changedFiles(range), loadTasks()).values()) console.log(reason);
}
