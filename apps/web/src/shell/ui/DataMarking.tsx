import { copy } from "../copy";
import type { Marking } from "../useDesignation";
import { Alert } from "./Alert";
import styles from "./DataMarking.module.css";
import { Badge } from "./StatusPill";

export interface DataMarkingProps {
  marking: Marking;
  variant: "badge" | "banner" | "cell";
  recordNoun?: string;
}

export function showsMarking(marking: Marking): boolean {
  return marking.kind === "cui" || marking.kind === "unavailable";
}

export function DataMarking({ marking, variant, recordNoun = "project" }: DataMarkingProps) {
  if (marking.kind === "cui") {
    if (variant === "banner") {
      return (
        <Alert tone="warning" title={copy.marking.badge}>
          {copy.marking.source(marking.source, recordNoun)}
        </Alert>
      );
    }
    return <Badge tone="warning">{copy.marking.badge}</Badge>;
  }
  if (marking.kind === "unavailable" && variant !== "badge") return <span className={styles.muted}>{copy.marking.unavailable}</span>;
  if (marking.kind === "none" && variant === "cell") return <span className={styles.muted}>{copy.marking.none}</span>;
  return null;
}
