import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation, type Location } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { CopilotHome } from "../../modules/copilot/pages";
import { Shell } from "../Shell";
import { AllFeaturesPage } from "../pages/AllFeaturesPage";
import { HomePage } from "../pages/HomePage";
import { AVAILABILITY_PATH } from "./availability";
import { availableAssistant, json, server, useFakeServer } from "../testing/fakeServer";

useFakeServer();

const CHAT_PATH = "/api/v1/copilot/chat";
const CONVERSATIONS_PATH = "/api/v1/agents/copilot/conversations";
const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const MESSAGES_PATH = `${CONVERSATIONS_PATH}/${CONVERSATION_ID}/messages`;
const CANARY = "CANARY-quarterly-cash-forecast-4417";

let locations: Location[] = [];

function Probe() {
  const location = useLocation();
  locations.push(location);
  return null;
}

async function renderApp(prompt: string, routeFails = false) {
  locations = [];
  server.handler = (url) =>
    url.pathname === CHAT_PATH
      ? routeFails
        ? json(503, { code: "AGT-0500" })
        : json(200, { kind: "ask", confidence: 1, generated: true })
      : url.pathname === CONVERSATIONS_PATH
        ? json(201, { conversationId: CONVERSATION_ID })
        : url.pathname === MESSAGES_PATH
          ? json(200, { answer: `Answer for ${CANARY}`, citations: [], toolCalls: [], refusal: null, generated: true })
          : url.pathname === AVAILABILITY_PATH
            ? json(200, availableAssistant)
            : undefined;
  render(
    <MemoryRouter initialEntries={["/"]}>
      <Probe />
      <Routes>
        <Route element={<Shell modules={[]} duties={["mcp.copilot.use"]} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/copilot" element={<CopilotHome />} />
          <Route path="/features" element={<AllFeaturesPage />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  return userEvent.setup().type(await screen.findByRole("textbox", { name: "Ask or do anything..." }), `${prompt}{Enter}`);
}

describe("assistant text stays out of browser-visible state on route change", () => {
  it("keeps a prompt and its answer out of the URL, history state, title and telemetry", async () => {
    await renderApp(CANARY);
    expect(await within(screen.getByRole("main")).findByRole("article", { name: "Copilot answer" })).toHaveTextContent(CANARY);

    expect(locations.every((l) => l.pathname === "/")).toBe(true);
    for (const location of locations) {
      expect(location.search).toBe("");
      expect(location.hash).toBe("");
      expect(location.state).toBeNull();
      expect(JSON.stringify(location)).not.toContain(CANARY);
    }
    expect(document.title).not.toContain(CANARY);

    const chats = server.calls.filter((c) => c.url.pathname === CHAT_PATH || c.url.pathname === MESSAGES_PATH);
    const others = server.calls.filter((c) => !chats.includes(c));
    for (const call of others) {
      expect(JSON.stringify({ url: call.url.href, headers: call.init.headers, body: call.body })).not.toContain(CANARY);
    }
    expect(chats.map((c) => c.body)).toEqual([
      { mode: "route", message: CANARY },
      { mode: "ask", message: CANARY },
    ]);
    expect(chats.every((c) => c.url.search === "")).toBe(true);
  });

  it("keeps the prompt out of the URL and history state when the search fallback opens the features page", async () => {
    await renderApp(CANARY, true);
    expect(await screen.findByRole("searchbox", { name: "Filter features" })).toHaveValue(CANARY);
    expect(locations.some((l) => l.pathname === "/features")).toBe(true);
    for (const location of locations) {
      expect(location.search).toBe("");
      expect(location.state).toBeNull();
      expect(JSON.stringify(location)).not.toContain(CANARY);
    }
    expect(document.title).not.toContain(CANARY);
  });
});
