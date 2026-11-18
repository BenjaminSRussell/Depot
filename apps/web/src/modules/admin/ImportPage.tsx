import { Card, PageHeader } from "../../shell/ui";
import { ImportStep } from "../setup/ImportStep";
import styles from "./pages.module.css";

/**
 * Import from the admin menu: the setup guide's "Import your first records" step (modules/setup/ImportStep.tsx), so
 * there is one import with a template, a row-by-row preview and a real run. It replaces a page that only counted CSV
 * columns next to a disabled Import button.
 */
export function ImportPage() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import records"
        description="Check a CSV file of vendors, customers or products, then import it. It is the same import as the Import your first records step of the setup guide."
      />
      <Card>
        <ImportStep />
      </Card>
      <p className={styles.muted}>Lists that can be exported, such as purchase orders and vendor invoices, have their own Export CSV button.</p>
    </div>
  );
}
