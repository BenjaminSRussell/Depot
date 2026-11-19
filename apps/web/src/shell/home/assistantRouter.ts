export const ASSISTANT_DUTY = "mcp.copilot.use";

export interface AssistantRoute {
  to: string;
  state: { prompt: string; mode: "ask" };
}

// TODO: E1-S21 replaces this body with the prompt router that dispatches to the answer (E1-S4), navigate (E1-S6) and propose-action (E1-S7) paths.
export async function routePrompt(text: string): Promise<AssistantRoute> {
  return { to: "/copilot", state: { prompt: text, mode: "ask" } };
}
