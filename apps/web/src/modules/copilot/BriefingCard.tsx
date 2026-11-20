import { useId, useState } from "react";
import { Link } from "react-router-dom";
import { copy } from "../../shell/copy";
import { formatDateTime } from "../../shell/format";
import { Alert, Badge, Button, Card, Skeleton } from "../../shell/ui";
import { briefingItemRoute, listBriefings, type Briefing, type BriefingItem } from "./api/briefings";
import styles from "./BriefingCard.module.css";
import { GENERATED_BADGE } from "./domain";
import { agentLabel } from "./suggestionModel";
import { useLoad } from "./useLoad";

interface SectionProps {
  title: string;
  items: BriefingItem[];
  defaultOpen: boolean;
}

function Item({ item }: { item: BriefingItem }) {
  const route = briefingItemRoute(item);
  return (
    <li>
      <span className={styles.source}>{copy.briefing.sourceLabel(agentLabel(item.agentId))}</span> {route === undefined ? item.text : <Link to={route}>{item.text}</Link>}
    </li>
  );
}

function Section({ title, items, defaultOpen }: SectionProps) {
  const [open, setOpen] = useState(defaultOpen);
  const panelId = useId();
  return (
    <div>
      <h3 className={styles.sectionHeading}>
        <Button variant="ghost" size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
          {copy.briefing.section(title, items.length)}
        </Button>
      </h3>
      <div id={panelId} hidden={!open}>
        {items.length === 0 ? (
          <p>{copy.briefing.nothing}</p>
        ) : (
          <ul className={styles.items} aria-label={title}>
            {items.map((item, i) => (
              <Item key={`${item.factId}-${i}`} item={item} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function BriefingBody({ briefing }: { briefing: Briefing }) {
  const generatedAt = briefing.completedAt ?? briefing.createdAt;
  return (
    <div className={styles.body}>
      <p className={styles.meta}>
        <span>{copy.briefing.generatedAt}</span>
        <time dateTime={generatedAt}>{formatDateTime(generatedAt)}</time>
        {briefing.narrative ? <Badge tone="info">{GENERATED_BADGE}</Badge> : null}
      </p>
      {briefing.narrative ? null : <p>{copy.briefing.deterministicNote}</p>}
      <Section title={copy.briefing.changes} items={briefing.changes} defaultOpen={false} />
      <Section title={copy.briefing.risks} items={briefing.risks} defaultOpen={false} />
      <Section title={copy.briefing.recommendedActions} items={briefing.recommendedActions} defaultOpen />
    </div>
  );
}

export function BriefingCard() {
  const state = useLoad(listBriefings, "briefings");
  const briefing = state.data?.items.find((b) => b.status === "READY");
  if (!state.loading && state.error === null && briefing === undefined) return null;
  return (
    <Card title={copy.briefing.title} headingLevel={2}>
      {state.loading ? (
        <div aria-busy="true">
          <span className="visually-hidden">{copy.briefing.loading}</span>
          <Skeleton height="tile" />
        </div>
      ) : state.error !== null ? (
        <Alert tone="danger" live="alert" title={copy.briefing.errorTitle} action={<Button onClick={state.reload}>{copy.briefing.retry}</Button>}>
          {copy.briefing.errorBody}
        </Alert>
      ) : briefing === undefined ? null : (
        <BriefingBody briefing={briefing} />
      )}
    </Card>
  );
}
