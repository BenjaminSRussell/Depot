import { useId, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { usePrincipalDuties } from "../../shell/principal";
import { copy } from "../../shell/copy";
import { Badge, Button } from "../../shell/ui";
import styles from "./AiSuggestion.module.css";
import { GENERATED_BADGE } from "./domain";
import {
  carriesCui,
  leftOutNotice,
  resolveRecord,
  type LeftOutInput,
  type SuggestionHost,
  type SuggestionViewModel,
} from "./suggestionModel";

export function AiMarking({ dataClass, excluded }: { dataClass?: string | undefined; excluded?: LeftOutInput | undefined }) {
  const notice = leftOutNotice(excluded);
  return (
    <>
      {carriesCui(dataClass) ? <Badge tone="warning">{copy.marking.badge}</Badge> : null}
      {notice === undefined ? null : <p>{notice}</p>}
    </>
  );
}

export interface AiSuggestionProps {
  host: SuggestionHost;
  suggestion: SuggestionViewModel;
  children?: ReactNode;
}

interface DisclosureProps {
  label: string;
  children: ReactNode;
}

function Disclosure({ label, children }: DisclosureProps) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  return (
    <div className={styles.disclosure}>
      <Button
        variant="ghost"
        size="sm"
        className={styles.toggle}
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen(!open)}
      >
        {label}
      </Button>
      <div id={panelId} className={styles.panel} hidden={!open}>
        {children}
      </div>
    </div>
  );
}

export function AiSuggestion({
  host,
  suggestion,
  children,
}: AiSuggestionProps) {
  const duties = usePrincipalDuties();
  const records = suggestion.records.map((record) =>
    resolveRecord(record, duties),
  );
  return (
    <div
      role="group"
      aria-label="AI suggestion"
      className={styles.suggestion}
      data-host={host}
    >
      <div>
        <Badge tone="info">{GENERATED_BADGE}</Badge> <AiMarking dataClass={suggestion.dataClass} excluded={suggestion.excluded} />
      </div>
      {children}
      <Disclosure label="Why this?">
        <p>
          Produced by {suggestion.agentLabel}
          {suggestion.agentVersion === undefined ? "" : `, version ${suggestion.agentVersion}`}
          .
        </p>
        {suggestion.rationale === undefined ? (
          <p>No rationale was recorded for this suggestion.</p>
        ) : (
          <p>{suggestion.rationale}</p>
        )}
        {suggestion.confidencePercent === undefined ? null : (
          <p>Confidence: {suggestion.confidencePercent}%</p>
        )}
      </Disclosure>
      <Disclosure label="Data used">
        <h4 className={styles.heading}>Tools called</h4>
        <ul className={styles.list} aria-label="Tools called">
          {suggestion.tools.map((tool, i) => (
            <li key={`${tool}-${i}`}>{tool}</li>
          ))}
        </ul>
        <h4 className={styles.heading}>Records read</h4>
        {records.length === 0 ? (
          <p>No source records were recorded for this suggestion.</p>
        ) : (
          <ul className={styles.list} aria-label="Records read">
            {records.map((record, i) => (
              <li key={`${record.entityLabel}-${i}`}>
                {record.href ? (
                  <Link
                    to={record.href}
                  >{`${record.entityLabel}: ${record.label}`}</Link>
                ) : (
                  record.entityLabel
                )}
              </li>
            ))}
          </ul>
        )}
      </Disclosure>
    </div>
  );
}
