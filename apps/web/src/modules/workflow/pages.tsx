import { Link } from "react-router-dom";
import { PageHeader } from "../../shell/ui";

/**
 * The workflow area's start page. It shows no job counts: the batch service has no run list to count from yet, and the
 * job monitor, designer and templates are in shell/plannedRoutes.json until it does.
 */
export function WorkflowHome() {
  return (
    <>
      <PageHeader title="Workflow" description="Approvals and other work waiting for you." />
      <p>
        <Link to="/workflow/inbox">Open My work</Link>
      </p>
    </>
  );
}
