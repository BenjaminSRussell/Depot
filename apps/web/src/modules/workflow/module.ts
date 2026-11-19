import { createElement } from "react";
import type { DepotModule } from "../registry";
import { InboxPage } from "./InboxPage";
import { WorkflowHome } from "./pages";

// TODO: B-34 returns /workflow/designer, /workflow/jobs and /workflow/templates.
const workflow: DepotModule = {
  id: "workflow",
  title: "Workflow and batch",
  description: "Approvals and the work waiting for you.",
  routes: [
    { path: "/workflow", element: createElement(WorkflowHome) },
    { path: "/workflow/inbox", element: createElement(InboxPage) },
  ],
  nav: [
    { label: "Workflow", to: "/workflow", duty: "workflow.read" },
    { label: "Inbox", to: "/workflow/inbox", duty: "inbox.read" },
  ],
};

export default workflow;
