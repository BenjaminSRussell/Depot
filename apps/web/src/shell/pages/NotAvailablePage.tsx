import { Link } from "react-router-dom";
import { copy } from "../copy";
import { Card, EmptyState, buttonClassName } from "../ui";

export function NotAvailablePage({ featureOff = false, planned = false }: { featureOff?: boolean; planned?: boolean }) {
  const isPlanned = planned && !featureOff;
  return (
    <Card>
      <EmptyState
        icon="lock"
        headingLevel={2}
        title={featureOff ? copy.notAvailable.featureOffTitle : isPlanned ? copy.notAvailable.plannedTitle : copy.notAvailable.title}
        description={featureOff ? copy.notAvailable.featureOffDescription : isPlanned ? copy.notAvailable.plannedDescription : copy.notAvailable.description}
        action={
          <Link to="/" className={buttonClassName("secondary")}>
            {copy.buttons.goHome}
          </Link>
        }
      />
    </Card>
  );
}
