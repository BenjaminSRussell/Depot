export const ASSISTANT_DUTY = "mcp.copilot.use";

export interface AssistantRoute {
  to: string;
  state: { prompt: string; mode: "ask" };
}

export async function routePrompt(text: string): Promise<AssistantRoute> {
  return { to: "/copilot", state: { prompt: text, mode: "ask" } };
}
