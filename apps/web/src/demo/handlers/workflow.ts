// Sample batch runs and message templates for the workflow screens. They used to be default props of the pages and
// so shipped in every build; here they exist only in the demo build. The job monitor and templates routes are in
// shell/plannedRoutes.json until the batch service can list runs and templates, so nothing reads these yet; the
// paths are the ones the workflow module's client calls (modules/workflow/api/client.ts).
import { route } from "../backend";

route("GET", "/api/v1/batch/runs", () => ({
  items: [
    { code: "rows-1m", status: "executing", rows: "400000 / 1000000", eta: "2 min" },
    { code: "outbox.sweep", status: "ended", rows: "128 / 128", eta: "—" },
  ],
}));

route("GET", "/api/v1/notify/templates", () => ({
  items: ["workflow.workitem.assigned", "workflow.workitem.escalated", "batch.run.failed"].map((code) => ({ code, channel: "in_app", language: "en" })),
}));
