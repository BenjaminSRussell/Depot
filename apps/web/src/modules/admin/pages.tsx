import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import { isPlanned } from "./PlannedPage";
import styles from "./pages.module.css";

// The audit page returns here when it reads the audit service (B-23). Until then /admin/audit, like the other routes in
// shell/plannedRoutes.json, renders PlannedPage and has no card.
export const adminCards = [
  { to: "/admin/audit", title: "Audit", description: "See who changed what, and when." },
  { to: "/admin/sequences", title: "Sequences", description: "See the number sequences of each legal entity." },
  { to: "/admin/alerts", title: "Alerts", description: "Review failures and overdue approvals." },
  { to: "/admin/import", title: "Import / export", description: "Import vendors, customers and products from a CSV file. Export from the list itself." },
  {
    to: "/admin/documents",
    title: "Documents",
    description: "Check documents in and out, track versions and apply legal holds.",
  },
  { to: "/admin/texts", title: "Standard texts", description: "Reusable text in every language you support." },
  { to: "/admin/security/permissions", title: "Effective permissions", description: "See what a user can and cannot do." },
  { to: "/admin/security/assignments", title: "Role assignments", description: "Assign, review and end roles." },
  { to: "/admin/security/legal-entities", title: "Legal entities", description: "The legal entities roles can be scoped to." },
];

export function AdminHome() {
  return (
    <>
      <PageHeader title="Admin" description="Number sequences, imports and who can do what, in one place." />
      <CardGrid as="ul" label="Admin pages">
        {adminCards
          .filter((card) => !isPlanned(card.to))
          .map((card) => (
            <li key={card.to} className={styles.cell}>
              <Card>
                <h3 className={styles.cardTitle}>
                  <Link to={card.to} className={styles.cardLink}>
                    {card.title}
                  </Link>
                </h3>
                <p className={styles.cardDescription}>{card.description}</p>
              </Card>
            </li>
          ))}
      </CardGrid>
    </>
  );
}
