import { createElement } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { copy } from "../../shell/copy";
import type { DepotModule } from "../registry";
import { AgentsPage } from "./AgentsPage";
import { BudgetsPage } from "./BudgetsPage";
import { KillSwitchPage } from "./KillSwitchPage";
import { CopilotHome } from "./pages";
import { ProposalPage } from "./ProposalPage";
import { ToolCatalogPage } from "./ToolCatalogPage";
import { ToolDetailPage } from "./ToolDetailPage";

function ToolCatalogRoute() {
  const navigate = useNavigate();
  return createElement(ToolCatalogPage, { onOpen: (name: string) => navigate(`/copilot/tools/${encodeURIComponent(name)}`) });
}

function ToolDetailRoute() {
  const { name = "" } = useParams();
  return createElement(ToolDetailPage, { name });
}

function ProposalRoute() {
  const { approvalId = "" } = useParams();
  return createElement(ProposalPage, { approvalId });
}

const copilot: DepotModule = {
  id: "copilot",
  title: "Copilot",
  description: "Ask questions, explain records and draft text with cited answers, see the tools the copilot can call for you, and halt agent runs with the kill switch.",
  routes: [
    { path: "/copilot", element: createElement(CopilotHome) },
    { path: "/copilot/tools", element: createElement(ToolCatalogRoute), handle: { title: "Tool catalog" } },
    { path: "/copilot/tools/:name", element: createElement(ToolDetailRoute), handle: { title: "Tool" } },
    { path: "/copilot/kill-switch", element: createElement(KillSwitchPage), handle: { title: "Agent kill switch" } },
    { path: "/copilot/budgets", element: createElement(BudgetsPage, {}), handle: { title: "AI controls" } },
    { path: "/copilot/agents", element: createElement(AgentsPage, {}), handle: { title: "Agents" } },
    { path: "/assistant/proposals/:approvalId", element: createElement(ProposalRoute), handle: { title: copy.proposal.pageTitle, duty: "agents.run.read" } },
  ],
  nav: [
    { label: "Copilot", to: "/copilot", duty: "mcp.copilot.use" },
    { label: "Tool catalog", to: "/copilot/tools", duty: "mcp.copilot.use" },
    { label: "Agent kill switch", to: "/copilot/kill-switch", duty: "agents.admin" },
    { label: "AI controls", to: "/copilot/budgets", duty: "agentbudget.read" },
    { label: "Agents", to: "/copilot/agents", duty: "agents.admin" },
  ],
};

export default copilot;
