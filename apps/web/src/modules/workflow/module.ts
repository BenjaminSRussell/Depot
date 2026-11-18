import { createElement } from "react";
import type { DepotModule } from "../registry";
import { plannedRoute } from "../admin/PlannedPage";
import { InboxPage } from "./InboxPage";
import { WorkflowHome } from "./pages";

const workflow: DepotModule = {
  id: "workflow",
  title: "Workflow and batch",
  description: "Approvals and other work waiting for you.",
  routes: [
    { path: "/workflow", element: createElement(WorkflowHome) },
    { path: "/workflow/inbox", element: createElement(InboxPage) },
    plannedRoute("/workflow/designer"),
    plannedRoute("/workflow/jobs"),
    plannedRoute("/workflow/templates"),
  ],
  // Planned routes keep their nav entries so bookmarks and the palette land on an honest "isn't available yet" page;
  // no workspace links them (shell/honestScreens.test.ts).
  nav: [
    { label: "Workflow", to: "/workflow", duty: "workflow.read" },
    { label: "Inbox", to: "/workflow/inbox", duty: "inbox.read" },
    { label: "Designer", to: "/workflow/designer", duty: "workflow.write" },
    { label: "Job monitor", to: "/workflow/jobs", duty: "batch.read" },
    { label: "Templates", to: "/workflow/templates", duty: "notify.write" },
  ],
};

export default workflow;
