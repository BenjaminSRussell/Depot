// Fails when a production build carries demo-only data (UXS-01, backlog B-21 AC3): the job monitor's old sample rows
// and the demo company live under src/demo/ and may reach only the demo build.
//
//   npm run build && node scripts/check-bundle.mjs [dist]
//
// CI runs it after `npm run build` (.github/workflows/ci.yml, job web). src/shell/honestScreens.test.ts holds the
// matching source scan, which fails earlier and names the source file.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const MARKERS = ["rows-1m", "400000 / 1000000", "workflow.workitem.escalated", "Cedar Ridge", "cedarridgepumps.example", "Jordan Avery"];

const root = process.argv[2] ?? "dist";
const files = [];
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path);
    else if (/\.(js|css|html|json|map)$/.test(name)) files.push(path);
  }
};
walk(root);
if (files.length === 0) {
  console.error(`check-bundle: no build output under ${root}; run npm run build first`);
  process.exit(2);
}
const found = files.flatMap((path) => {
  const text = readFileSync(path, "utf8");
  return MARKERS.filter((marker) => text.includes(marker)).map((marker) => `${path}: ${marker}`);
});
if (found.length > 0) {
  console.error(`check-bundle: demo-only data in the production build:\n${found.join("\n")}`);
  process.exit(1);
}
console.log(`check-bundle: ${files.length} files, no demo-only data`);
