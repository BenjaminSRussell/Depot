import { createElement } from "react";
import type { DepotModule } from "../registry";
import { plannedRoute } from "../../shell/pages/PlannedPage";
import { InboxPage } from "./InboxPage";
import { WorkflowHome } from "./pages";

const workflow: DepotModule = {
  id: "workflow",
  title: "Workflow",
  description: "Approvals and other work waiting for you.",
  routes: [
    { path: "/workflow", element: createElement(WorkflowHome) },
    { path: "/workflow/inbox", element: createElement(InboxPage) },
    plannedRoute("/workflow/designer"),
    plannedRoute("/workflow/jobs"),
    plannedRoute("/workflow/templates"),
  ],
  // Entries for routes in shell/plannedRoutes.json stay here for the nav audit and the page title, but the shell drops
  // them from the sidebar, workspaces, All features and the palette (shell/pages/PlannedPage.tsx, withoutPlannedNav).
  nav: [
    { label: "Workflow", to: "/workflow", duty: "workflow.read" },
    { label: "Inbox", to: "/workflow/inbox", duty: "inbox.read" },
    { label: "Designer", to: "/workflow/designer", duty: "workflow.write" },
    { label: "Job monitor", to: "/workflow/jobs", duty: "batch.read" },
    { label: "Templates", to: "/workflow/templates", duty: "notify.write" },
  ],
};

export default workflow;
