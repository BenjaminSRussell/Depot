import { ImportStep } from "../setup/ImportStep";
import { Card, PageHeader } from "../../shell/ui";

export function ImportPage() {
  return (
    <>
      <PageHeader title="Import" description="Bring your records in from a file. Check the file first, then start the import." />
      <Card>
        <ImportStep />
      </Card>
    </>
  );
}
