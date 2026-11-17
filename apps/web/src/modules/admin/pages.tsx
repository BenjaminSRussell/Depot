import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const adminCards = [
  { to: "/admin/sequences", title: "Sequences", description: "See how your documents are numbered, per legal entity." },
  { to: "/admin/import", title: "Import", description: "Bring your records in from a file." },
  { to: "/admin/security/permissions", title: "Effective permissions", description: "See what a user can and cannot do." },
  { to: "/admin/security/assignments", title: "Role assignments", description: "Assign, review and end roles." },
  { to: "/admin/security/legal-entities", title: "Legal entities", description: "The legal entities roles can be scoped to." },
];

export function AdminHome() {
  return (
    <>
      <PageHeader title="Admin" description="Numbering, imports and security in one place." />
      <CardGrid as="ul" label="Admin pages">
        {adminCards.map((card) => (
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
