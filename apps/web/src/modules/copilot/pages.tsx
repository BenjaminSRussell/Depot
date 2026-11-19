import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { PageHeader } from "../../shell/ui";
import { CopilotPanel } from "./CopilotPanel";
import styles from "./pages.module.css";

const MAX_PROMPT_CHARS = 4000;

function promptFrom(state: unknown): string | undefined {
  const prompt = (state as { prompt?: unknown } | null)?.prompt;
  return typeof prompt === "string" && prompt.trim() !== "" && prompt.length <= MAX_PROMPT_CHARS ? prompt : undefined;
}

export function CopilotHome() {
  const location = useLocation();
  const navigate = useNavigate();
  const [initialMessage] = useState(() => promptFrom(location.state));
  useEffect(() => {
    if (location.state !== null && location.state !== undefined) navigate(".", { replace: true, state: null });
  }, [location.state, navigate]);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Copilot"
        description="Ask about your data, explain a record, draft text or summarize a workflow. Every answer lists the tools it used and the sources it cites, and runs with your permissions."
      />
      <CopilotPanel initialMode="ask" {...(initialMessage ? { initialMessage } : {})} />
    </div>
  );
}
