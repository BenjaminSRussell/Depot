import { readFileSync, realpathSync } from "node:fs";
import { isAbsolute, relative, resolve } from "node:path";
import { changedFiles, computeScope, isRange, loadTasks } from "./usability-scope.mjs";

const DEFAULT_LINE = "no screen on a task path changed";
const REPORT_COLUMNS = ["pass", "median time", "errors", "interactions"];
const PROVISIONAL_USES = 3;
const PROVISIONAL_DAYS = 90;
const DAY_MS = 24 * 60 * 60 * 1000;
const REPORT_DIR = "apps/web/docs";
const REPO_ROOT = realpathSync(resolve(import.meta.dirname, "../../.."));

const LINES = {
  notInScope: /^Not in scope: (.+?)\.?$/,
  passed: /^Passed: tasks (.+), report (\S+?)\.?$/,
  waived: /^Waived: ([^,]+), ([^,]+), ([^,]+), tasks (.+), approved by ([^,]+), product owner\.?$/,
  provisional: /^Provisional: tasks (.+?)(?:, Round 0 (\S+?))?(?:, re-walk (\S+?))?(?:, signed (.+?))?\.?$/,
};

export function parseUsabilitySection(text) {
  const heading = /^## Usability gate[ \t]*$/m.exec(text);
  if (!heading) return null;
  const body = text.slice(heading.index + heading[0].length).split(/^## /m)[0];
  const lines = body.replace(/<!--[\s\S]*?-->/g, "").split("\n").map((l) => l.trim()).filter(Boolean);
  const date = /^Release date: (\d{4}-\d{2}-\d{2})/m.exec(text)?.[1];
  const time = date ? Date.parse(`${date}T00:00:00Z`) : NaN;
  const valid = Number.isFinite(time) && new Date(time).toISOString().startsWith(date);
  return { lines, releaseDate: valid ? time : null };
}

const idList = (text) => text.split(",").map((s) => s.trim());

export function reportRow(report, id) {
  let columns = null;
  for (const line of report.split("\n")) {
    if (!line.trim().startsWith("|")) continue;
    const cells = line.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
    const found = REPORT_COLUMNS.map((name) => cells.findIndex((c) => c.toLowerCase().startsWith(name)));
    if (found.every((i) => i >= 0)) columns = found;
    else if (columns && cells[0] === id) return columns.map((i) => cells[i]);
  }
  return null;
}

const isMeasure = (cell) => /^\d+(\.\d+)?%?$/.test(cell) && Number.isFinite(Number(cell.replace("%", "")));

function provisionalUses(previous, tasks) {
  const personaOf = new Map(tasks.map((t) => [t.id, t.persona]));
  const uses = new Map();
  for (const notes of previous) {
    const section = parseUsabilitySection(notes);
    for (const line of section?.lines ?? []) {
      const match = LINES.provisional.exec(line);
      if (!line.startsWith("Provisional:") || !match) continue;
      for (const persona of new Set(idList(match[1]).map((id) => personaOf.get(id)).filter(Boolean))) {
        uses.set(persona, [...(uses.get(persona) ?? []), section.releaseDate]);
      }
    }
  }
  return uses;
}

function waivedForNoReference(notes) {
  const lines = parseUsabilitySection(notes ?? "")?.lines ?? [];
  return lines.flatMap((line) => {
    const match = LINES.waived.exec(line);
    return match && match[1].trim().toLowerCase() === "no reference" ? idList(match[4]) : [];
  });
}

export function checkGate({ notes, scope, tasks, report, previous = [], exists }) {
  const reasons = [];
  const notices = [];
  const known = new Set(tasks.map((t) => t.id));
  const section = notes == null ? null : parseUsabilitySection(notes);
  const covered = new Map();
  const cover = (ids, how) => {
    for (const id of ids) {
      if (!known.has(id)) reasons.push(`${how}: "${id}" is not a task id`);
      else covered.set(id, [...(covered.get(id) ?? []), how]);
    }
  };

  if (previous.length === 0) notices.push("no previous release notes: G7 and G8 counted against zero previous releases");
  if (!section) reasons.push("the release notes have no Usability gate section (G4)");
  else if (section.lines.length === 0) reasons.push("the Usability gate section is empty (G4)");

  const uses = provisionalUses(previous, tasks);
  const personaOf = new Map(tasks.map((t) => [t.id, t.persona]));
  const lastWaived = waivedForNoReference(previous.at(-1));

  for (const line of section?.lines ?? []) {
    let match;
    if ((match = LINES.notInScope.exec(line))) {
      if (scope.size > 0) reasons.push(`Not in scope is not valid: ${[...scope.keys()].join(", ")} are in scope (G2)`);
      else if (match[1].toLowerCase().includes(DEFAULT_LINE)) reasons.push("the default Not in scope text is not accepted (G4)");
    } else if ((match = LINES.passed.exec(line))) {
      const ids = idList(match[1]);
      cover(ids, "Passed");
      const text = report(match[2]);
      if (text == null) reasons.push(`Passed: report ${match[2]} was not found inside the repository's committed reports (G3)`);
      else {
        for (const id of ids.filter((i) => known.has(i))) {
          const row = reportRow(text, id);
          if (!row || !row.every(isMeasure)) reasons.push(`${id}: the report has no numeric Pass, Median time, Errors and Interactions row (G3)`);
        }
      }
    } else if (line.startsWith("Waived:")) {
      match = LINES.waived.exec(line);
      if (!match) reasons.push(`the waiver needs measure, cause, issue, tasks, approved by name, product owner (G7): ${line}`);
      else {
        const ids = idList(match[4]);
        cover(ids, "Waived");
        const measure = match[1].trim().toLowerCase();
        if (measure === "blocker") reasons.push("a blocker cannot be waived (G7)");
        if (measure === "no reference") {
          for (const id of ids.filter((i) => lastWaived.includes(i))) reasons.push(`${id} was already waived for no reference in the previous release (G7)`);
        }
      }
    } else if (line.startsWith("Provisional:")) {
      match = LINES.provisional.exec(line);
      if (!match) {
        reasons.push(`the provisional entry needs tasks, Round 0 link, re-walk link and signed name (G8): ${line}`);
        continue;
      }
      const ids = idList(match[1]);
      cover(ids, "Provisional");
      const missing = [["Round 0 link", match[2]], ["re-walk link", match[3]], ["signed name", match[4]]].filter(([, v]) => !v).map(([k]) => k);
      for (const [label, link] of [["Round 0", match[2]], ["re-walk", match[3]]]) {
        if (link && !exists(link)) reasons.push(`${ids.join(", ")}: the ${label} link ${link} does not exist inside the repository (G8)`);
      }
      if (missing.length) reasons.push(`${ids.join(", ")}: the provisional entry is missing the ${missing.join(", ")} (G8)`);
      if (section.releaseDate == null) reasons.push("the release date is missing or not a real date, so the provisional window cannot be checked (G8)");
      for (const persona of new Set(ids.map((id) => personaOf.get(id)).filter(Boolean))) {
        const dates = uses.get(persona) ?? [];
        const first = dates.includes(null) ? null : Math.min(...dates);
        const elapsed = first === null || section.releaseDate === null ? NaN : section.releaseDate - first;
        if (dates.length >= PROVISIONAL_USES) reasons.push(`${persona}: provisional reference already used in ${dates.length} releases, the window is closed (G8)`);
        else if (dates.length && !(elapsed >= 0 && elapsed <= PROVISIONAL_DAYS * DAY_MS)) {
          reasons.push(`${persona}: the provisional window of ${PROVISIONAL_DAYS} days from its first use is closed or its first date is unknown (G8)`);
        }
      }
    } else reasons.push(`unknown line in the Usability gate section: ${line}`);
  }

  for (const [id, hows] of covered) if (hows.length > 1) reasons.push(`${id} is covered by more than one line (${hows.join(", ")})`);
  for (const [id, reason] of scope) {
    if (!covered.has(id)) reasons.push(`${reason} and has no reference measurement (G2)`);
  }
  if (reasons.length) reasons.push(`${reasons.length} problem(s) found: the release is not recorded`);
  return { ok: reasons.length === 0, reasons, notices };
}

function resolveInRepo(link) {
  if (!link || isAbsolute(link) || /^[a-z][a-z\d+.-]*:/i.test(link)) return null;
  try {
    const real = realpathSync(resolve(REPO_ROOT, link));
    const rel = relative(REPO_ROOT, real);
    return rel && !rel.startsWith("..") && !isAbsolute(rel) ? real : null;
  } catch {
    return null;
  }
}

function inside(directory, path) {
  const rel = relative(directory, path);
  return rel !== "" && !rel.startsWith("..") && !isAbsolute(rel);
}

function readOrNull(path) {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return null;
  }
}

function main(argv) {
  const options = { previous: [], reportDir: REPORT_DIR };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--notes") options.notes = argv[++i];
    else if (argv[i] === "--range") options.range = argv[++i];
    else if (argv[i] === "--previous") options.previous.push(argv[++i]);
    else if (argv[i] === "--report-dir") options.reportDir = argv[++i];
    else return usage(`unknown argument ${argv[i]}`);
  }
  if (!options.notes || !options.range) return usage("--notes and --range are required");
  if (!isRange(options.range)) return usage(`--range must look like <from>..<to>, got ${options.range}`);

  const notes = readOrNull(options.notes);
  if (notes == null) return fail(`no release notes at ${options.notes}`);
  const previous = [];
  for (const path of options.previous) {
    const text = readOrNull(path);
    if (text == null) return fail(`cannot read previous release notes ${path}`);
    previous.push(text);
  }

  let scope;
  let tasks;
  try {
    tasks = loadTasks();
    scope = computeScope(changedFiles(options.range), tasks);
  } catch (error) {
    return fail(`cannot work out the scope for ${options.range}: ${error.message}`);
  }
  const reportDir = resolve(REPO_ROOT, options.reportDir);
  const report = (link) => {
    const path = resolveInRepo(link);
    return path && inside(reportDir, path) ? readOrNull(path) : null;
  };
  const result = checkGate({ notes, scope, tasks, report, previous, exists: (link) => resolveInRepo(link) !== null });
  for (const notice of result.notices) console.log(notice);
  for (const reason of result.reasons) console.error(reason);
  if (result.ok) console.log(`usability gate passed for ${scope.size} task(s) in scope`);
  return result.ok ? 0 : 1;
}

function usage(message) {
  console.error(`${message}\nusage: node scripts/check-usability-gate.mjs --notes <file> --range <from>..<to> [--previous <file>...] [--report-dir <dir>]`);
  return 2;
}

function fail(message) {
  console.error(message);
  return 1;
}

if (process.argv[1] === import.meta.filename) process.exit(main(process.argv.slice(2)));
