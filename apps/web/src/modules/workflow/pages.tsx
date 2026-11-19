import { Link } from "react-router-dom";
import { PageHeader } from "../../shell/ui";

export function WorkflowHome() {
  return (
    <>
      <PageHeader title="Workflow and batch" description="Approvals and the work waiting for you." />
      <p>
        <Link to="/workflow/inbox">Open My work</Link>
      </p>
    </>
  );
}
