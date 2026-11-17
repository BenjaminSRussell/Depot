import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { checkGate, parseUsabilitySection } from "./check-usability-gate.mjs";
import { SHELL_SENTINELS, computeScope, loadTasks } from "./usability-scope.mjs";

const tasks = loadTasks();
const fixture = (name) => readFileSync(join(import.meta.dirname, "fixtures/releases", name), "utf8");
const notesWith = (lines, date = "2026-11-17") => `Release date: ${date}.\n\n## Usability gate\n\n${lines}\n`;
const provisional = (date) => notesWith("Provisional: tasks AP4, Round 0 a.md, re-walk b.md, signed Jordan Avery", date);
const AP_SCOPE = computeScope(["apps/web/src/modules/ap-invoices/pages/PostInvoicePage.tsx"], tasks);
const check = (over) => checkGate({ notes: "", scope: new Map(), tasks, report: () => null, previous: [], exists: () => true, ...over });
const text = (result) => result.reasons.join("\n");

describe("computeScope (G1)", () => {
  it("brings in the tasks of a changed module", () => {
    expect([...AP_SCOPE.keys()]).toEqual(["AP1", "AP2", "AP3"]);
    expect(AP_SCOPE.get("AP1")).toBe("AP1 is in scope (apps/web/src/modules/ap-invoices changed)");
  });

  it("brings in the web sentinels for a shell change and ignores test files", () => {
    expect([...computeScope(["apps/web/src/shell/Shell.tsx"], tasks).keys()].sort()).toEqual([...SHELL_SENTINELS].sort());
    expect(computeScope(["apps/web/src/shell/Shell.test.tsx"], tasks).size).toBe(0);
  });

  it("brings in portal and device tasks only through their own folders", () => {
    expect([...computeScope(["apps/customer-portal/src/App.tsx"], tasks).keys()]).toEqual(["CP0", "CP1", "CP2", "CP3", "CP4", "CP5"]);
    expect([...computeScope(["apps/web/src/modules/mobile-flows/x.ts"], tasks).keys()].sort()).toEqual(["WH6", "WH7", "WH8", "WH9", "WS1", "WS2"]);
    expect(computeScope(["apps/web/src/shell/shared/Button.tsx"], tasks).has("CP1")).toBe(false);
  });

  it("finds nothing for a docs-only change", () => {
    expect(computeScope(["docs/ux/usability-programme.md", "apps/web/docs/usability-gate.md"], tasks).size).toBe(0);
  });
});

describe("checkGate", () => {
  it("fails Not in scope for a change under ap-invoices and names the tasks and the folder", () => {
    const result = check({ notes: fixture("not-in-scope.md"), scope: AP_SCOPE });
    expect(result.ok).toBe(false);
    for (const id of ["AP1", "AP2", "AP3"]) expect(text(result)).toContain(`${id} is in scope (apps/web/src/modules/ap-invoices changed)`);
    expect(text(result)).toContain("Not in scope is not valid");
  });

  it("accepts a correctly signed provisional entry", () => {
    const result = check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE, previous: [fixture("previous/2026-11-01.md")] });
    expect(result).toMatchObject({ ok: true, reasons: [] });
  });

  it("says when there are no previous release notes", () => {
    const result = check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE });
    expect(result.ok).toBe(true);
    expect(result.notices.join("\n")).toContain("zero previous releases");
  });

  it("rejects a provisional entry without a signature, naming the tasks", () => {
    const notes = notesWith("Provisional: tasks AP1, AP2, AP3, Round 0 a.md, re-walk b.md");
    const result = check({ notes, scope: AP_SCOPE });
    expect(result.ok).toBe(false);
    expect(text(result)).toContain("AP1, AP2, AP3: the provisional entry is missing the signed name");
  });

  it("rejects a provisional entry whose link does not exist", () => {
    const result = check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE, exists: (link) => !link.endsWith("passed.md") });
    expect(result.ok).toBe(false);
    expect(text(result)).toContain("the re-walk link apps/web/scripts/fixtures/releases/passed.md does not exist");
  });

  it("rejects a provisional entry that is not a task list at all", () => {
    expect(check({ notes: notesWith("Provisional: signed Jordan") }).ok).toBe(false);
  });

  it("closes the provisional window after three earlier uses", () => {
    const previous = ["2026-11-01", "2026-11-05", "2026-11-09"].map(provisional);
    const result = check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE, previous });
    expect(result.ok).toBe(false);
    expect(text(result)).toContain("ap-clerk: provisional reference already used in 3 releases");
  });

  it("closes the provisional window 90 days after the first use", () => {
    const result = check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE, previous: [provisional("2026-08-01")] });
    expect(result.ok).toBe(false);
    expect(text(result)).toContain("90 days");
  });

  it("fails closed when an earlier provisional use has no release date", () => {
    const undated = notesWith("Provisional: tasks AP4, Round 0 a.md, re-walk b.md, signed X").replace(/^Release date.*\n/, "");
    expect(check({ notes: fixture("provisional-signed.md"), scope: AP_SCOPE, previous: [undated] }).ok).toBe(false);
  });

  it("fails a provisional entry when the release date is missing or not a real date", () => {
    const line = "Provisional: tasks AP1, AP2, AP3, Round 0 a.md, re-walk b.md, signed X";
    expect(check({ notes: notesWith(line, "2026-13-45"), scope: AP_SCOPE }).ok).toBe(false);
    expect(check({ notes: `## Usability gate\n\n${line}\n`, scope: AP_SCOPE }).ok).toBe(false);
  });

  it.each([
    ["an empty section", "## Usability gate\n\n<!-- only a comment -->\n"],
    ["no section", "# Depot v0.0.1\n"],
  ])("fails with %s (G4)", (_name, notes) => {
    expect(check({ notes }).ok).toBe(false);
  });

  it("fails the old default line, with or without tasks in scope (G4)", () => {
    const notes = notesWith("Not in scope: no screen on a task path changed.");
    expect(text(check({ notes }))).toContain("default Not in scope text");
    expect(check({ notes, scope: AP_SCOPE }).ok).toBe(false);
  });

  it("accepts a reasoned Not in scope when nothing is in scope", () => {
    expect(check({ notes: notesWith("Not in scope: only release tooling changed.") }).ok).toBe(true);
  });

  it("fails a line it does not know, quoting it", () => {
    expect(text(check({ notes: notesWith("All good.") }))).toContain("unknown line in the Usability gate section: All good.");
  });

  it("fails a task in scope that no line covers (G2)", () => {
    const result = check({ notes: notesWith("Not in scope: x."), scope: AP_SCOPE });
    expect(text(result)).toContain("AP2 is in scope (apps/web/src/modules/ap-invoices changed) and has no reference measurement");
  });

  it("fails a task covered by two lines", () => {
    const lines = "Waived: no reference, cause, #1, tasks AP1, approved by Sam, product owner.\nProvisional: tasks AP1, Round 0 a.md, re-walk b.md, signed X";
    expect(text(check({ notes: notesWith(lines) }))).toContain("AP1 is covered by more than one line");
  });
});

describe("checkGate Passed (G3)", () => {
  const report = fixture("report.md");
  const passed = fixture("passed.md");
  const run = (reportText) => check({ notes: passed, scope: AP_SCOPE, report: () => reportText });

  it("accepts numeric rows for every task", () => {
    expect(run(report).ok).toBe(true);
  });

  it("fails when the report is not found", () => {
    expect(text(run(null))).toContain("was not found inside the repository");
  });

  it("fails naming a task with no row", () => {
    const withoutAp2 = report.replace(/^\| AP2 .*\n/m, "");
    expect(text(run(withoutAp2))).toContain("AP2: the report has no numeric");
  });

  it.each(["", "n/a", "pending", "not measured", "NaN", "1e400", "-3"])("fails when a cell reads %j", (cell) => {
    const broken = report.replace(/^\| AP3 \| 5 \| 0 \| 0 \| 40 /m, `| AP3 | 5 | 0 | 0 | ${cell} `);
    expect(text(run(broken))).toContain("AP3: the report has no numeric");
  });

  it("names a task id that does not exist", () => {
    const result = check({ notes: notesWith("Passed: tasks ap1, report r.md."), report: () => report });
    expect(text(result)).toContain('"ap1" is not a task id');
  });
});

describe("checkGate Waived (G7)", () => {
  const waiver = (measure, tail = ", product owner") => notesWith(`Waived: ${measure}, slow screen, #12, tasks AP1, approved by Sam Lee${tail}.`);
  const scope = new Map([["AP1", "AP1 is in scope (x changed)"]]);

  it("accepts a narrow waiver", () => {
    expect(check({ notes: waiver("time on task"), scope }).ok).toBe(true);
  });

  it("never waives a blocker", () => {
    expect(text(check({ notes: waiver("blocker"), scope }))).toContain("a blocker cannot be waived");
  });

  it("needs the product owner role", () => {
    expect(text(check({ notes: waiver("time on task", ""), scope }))).toContain("product owner");
  });

  it("does not waive the same task for no reference twice in a row", () => {
    const result = check({ notes: waiver("no reference"), scope, previous: [waiver("no reference")] });
    expect(text(result)).toContain("AP1 was already waived for no reference in the previous release");
    expect(check({ notes: waiver("no reference"), scope }).ok).toBe(true);
  });
});

describe("parseUsabilitySection", () => {
  it("reads the lines of the section only, without comments", () => {
    const section = parseUsabilitySection("Release date: 2026-11-17.\n\n## Usability gate\n\n<!-- a\nb -->\n\nPassed: x\n\n## Rollback\n\nother\n");
    expect(section.lines).toEqual(["Passed: x"]);
    expect(section.releaseDate).toBe(Date.parse("2026-11-17T00:00:00Z"));
  });
});

describe("check-usability-gate CLI", () => {
  const script = join(import.meta.dirname, "check-usability-gate.mjs");
  const run = (...args) => spawnSync(process.execPath, [script, ...args], { encoding: "utf8" });
  const notesFile = (content) => {
    const file = join(mkdtempSync(join(tmpdir(), "gate-")), "notes.md");
    writeFileSync(file, content);
    return file;
  };
  const passedPath = join(import.meta.dirname, "fixtures/releases/passed.md");

  it("exits 2 without --notes and --range", () => {
    expect(run().status).toBe(2);
    expect(run("--notes", passedPath).status).toBe(2);
  });

  it("exits 2 for a malformed range", () => {
    expect(run("--notes", passedPath, "--range", "abc").status).toBe(2);
    expect(run("--notes", passedPath, "--range", "a..").status).toBe(2);
  });

  it("exits 1 with no release notes when the notes file is absent", () => {
    const result = run("--notes", join(tmpdir(), "no-such-release-notes.md"), "--range", "HEAD..HEAD");
    expect(result.status).toBe(1);
    expect(result.stderr).toContain("no release notes");
  });

  it("exits 1 when git cannot resolve the range", () => {
    expect(run("--notes", passedPath, "--range", "no-such-rev-a..no-such-rev-b").status).toBe(1);
  });

  it("passes notes that link a real report inside the repository", () => {
    const result = run("--notes", passedPath, "--range", "HEAD..HEAD");
    expect(result.status).toBe(0);
    expect(result.stdout).toContain("zero previous releases");
  });

  it("fails a report link that leaves the repository", () => {
    const outside = notesFile(notesWith("Passed: tasks AP1, report ../../../../../../../../etc/hosts."));
    const result = run("--notes", outside, "--range", "HEAD..HEAD");
    expect(result.status).toBe(1);
    expect(result.stderr).toContain("was not found inside the repository");
  });

  it("fails an empty notes file", () => {
    expect(run("--notes", notesFile(""), "--range", "HEAD..HEAD").status).toBe(1);
  });
});
