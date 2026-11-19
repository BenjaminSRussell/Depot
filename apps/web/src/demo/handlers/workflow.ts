import { route } from "../backend";

route("GET", "/api/v1/batch/runs", () => ({
  items: [
    { code: "rows-1m", status: "executing", rows: "400000 / 1000000", eta: "2 min" },
    { code: "outbox.sweep", status: "ended", rows: "128 / 128", eta: "—" },
  ],
}));

route("GET", "/api/v1/notify/templates", () => ({
  items: ["workflow.workitem.assigned", "workflow.workitem.escalated", "batch.run.failed"].map((code) => ({ code, shipped: true })),
}));
