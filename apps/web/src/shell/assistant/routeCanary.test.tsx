import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation, type Location } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { CopilotHome } from "../../modules/copilot/pages";
import { Shell } from "../Shell";
import { HomePage } from "../pages/HomePage";
import { AVAILABILITY_PATH } from "./availability";
import { json, server, useFakeServer } from "../testing/fakeServer";

useFakeServer();

const CHAT_PATH = "/api/v1/copilot/chat";
const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};
const CANARY = "CANARY-quarterly-cash-forecast-4417";

let locations: Location[] = [];

function Probe() {
  const location = useLocation();
  locations.push(location);
  return null;
}

async function renderApp(prompt: string) {
  locations = [];
  server.handler = (url) =>
    url.pathname === CHAT_PATH
      ? json(200, { answer: `Answer for ${CANARY}`, citations: [], toolCalls: [], refusal: null, generated: true })
      : url.pathname === AVAILABILITY_PATH
        ? json(200, AVAILABLE)
        : undefined;
  render(
    <MemoryRouter initialEntries={["/"]}>
      <Probe />
      <Routes>
        <Route element={<Shell modules={[]} duties={["mcp.copilot.use"]} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/copilot" element={<CopilotHome />} />
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

    const arrived = locations.filter((l) => l.pathname === "/copilot");
    expect(arrived.length).toBeGreaterThan(0);
    for (const location of locations) {
      expect(location.search).toBe("");
      expect(location.hash).toBe("");
      expect(location.state).toBeNull();
      expect(JSON.stringify(location)).not.toContain(CANARY);
    }
    expect(document.title).not.toContain(CANARY);

    const others = server.calls.filter((c) => c.url.pathname !== CHAT_PATH);
    for (const call of others) {
      expect(JSON.stringify({ url: call.url.href, headers: call.init.headers, body: call.body })).not.toContain(CANARY);
    }
    const chats = server.calls.filter((c) => c.url.pathname === CHAT_PATH);
    expect(chats).toHaveLength(1);
    expect(chats[0].url.search).toBe("");
    expect(chats[0].body).toEqual({ mode: "ask", message: CANARY });
  });
});
