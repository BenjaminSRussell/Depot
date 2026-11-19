import { useEffect, useState } from "react";
import { clearHandedOffPrompt, peekHandedOffPrompt } from "../../shell/assistant/session";
import { AssistantNotice } from "../../shell/assistant/AvailabilityProvider";
import { PageHeader } from "../../shell/ui";
import { CopilotPanel } from "./CopilotPanel";
import styles from "./pages.module.css";

export function CopilotHome() {
  const [initialMessage] = useState(() => peekHandedOffPrompt());
  useEffect(() => {
    clearHandedOffPrompt();
  }, []);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Copilot"
        description="Ask about your data, explain a record, draft text or summarize a workflow. Every answer lists the tools it used and the sources it cites, and runs with your permissions."
      />
      <AssistantNotice />
      <CopilotPanel initialMode="ask" {...(initialMessage ? { initialMessage } : {})} />
    </div>
  );
}
