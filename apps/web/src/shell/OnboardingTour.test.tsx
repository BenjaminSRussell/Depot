import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { copy } from "./copy";
import { readOnboarding } from "./onboarding";
import { HomePage } from "./pages/HomePage";
import { noPreferenceStorage, type PreferenceStorage } from "./preferenceStorage";
import { Shell } from "./Shell";
import { availableAssistant, pageViolations } from "./testing/fakeServer";
import { completedOnboardingStorage, memoryStorage } from "./testing/onboardingFixtures";

const COPILOT_DUTY = "mcp.copilot.use";
const IDENTITY = "t.u1";
const PROMPT = "Ask or do anything...";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

interface Options {
  path?: string;
  duties?: string[];
  storage?: PreferenceStorage;
  availability?: Promise<Response>;
}

function renderTour({ path = "/", duties = [COPILOT_DUTY], storage = memoryStorage(), availability }: Options = {}) {
  const reply = () => new Response(JSON.stringify(availableAssistant), { status: 200, headers: { "Content-Type": "application/json" } });
  vi.stubGlobal("fetch", vi.fn(async () => availability ?? reply()));
  const tree = (
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<Shell modules={[]} duties={duties} identity={IDENTITY} onboardingStorage={storage} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="*" element={<p>Other page</p>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
  return { storage, view: render(tree), tree };
}

const tourDialog = () => screen.findByRole("dialog", { name: copy.onboarding.steps.prompt.title });
const button = (name: string) => screen.getByRole("button", { name });

describe("first-run tour", () => {
  it("starts for a new user with the step count, Next, Back and Skip, as a labelled dialog", async () => {
    renderTour();
    const dialog = await tourDialog();
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(within(dialog).getByText("Step 1 of 4")).toBeInTheDocument();
    expect(within(dialog).getByRole("status")).toHaveTextContent("Step 1 of 4: Ask or do anything");
    expect(button("Back")).toBeDisabled();
    expect(button("Next")).toHaveFocus();
    expect(button("Skip tour")).toBeInTheDocument();
  });

  it("moves with Next, Back and the arrow keys and announces each step", async () => {
    const user = userEvent.setup();
    renderTour();
    const dialog = await tourDialog();
    await user.click(button("Next"));
    expect(within(dialog).getByRole("status")).toHaveTextContent(`Step 2 of 4: ${copy.onboarding.steps.commandBar.title}`);
    await user.keyboard("{ArrowRight}");
    expect(within(dialog).getByRole("status")).toHaveTextContent(`Step 3 of 4: ${copy.onboarding.steps.approvals.title}`);
    expect(dialog).toHaveTextContent("can't do more than your own permissions allow");
    expect(dialog).toHaveTextContent("posts or touches money needs your approval");
    await user.keyboard("{ArrowLeft}{ArrowLeft}{ArrowLeft}");
    expect(within(dialog).getByText("Step 1 of 4")).toBeInTheDocument();
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
  });

  it("does not finish on the right arrow at the last step", async () => {
    const user = userEvent.setup();
    const { storage } = renderTour();
    await tourDialog();
    await user.keyboard("{ArrowRight}{ArrowRight}{ArrowRight}{ArrowRight}{ArrowRight}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(readOnboarding(IDENTITY, storage)).toBe("none");
  });

  it("keeps Tab inside the dialog", async () => {
    const user = userEvent.setup();
    renderTour();
    const dialog = await tourDialog();
    for (let press = 0; press < 6; press += 1) {
      await user.tab();
      expect(dialog).toContainElement(document.activeElement as HTMLElement);
    }
  });

  it("records skipped on Skip and does not start again on the next visit", async () => {
    const user = userEvent.setup();
    const { storage, view, tree } = renderTour();
    await tourDialog();
    await user.click(button("Skip tour"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(readOnboarding(IDENTITY, storage)).toBe("skipped");
    view.unmount();
    render(tree);
    await screen.findByRole("textbox", { name: PROMPT });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("records skipped on Escape", async () => {
    const user = userEvent.setup();
    const { storage } = renderTour();
    await tourDialog();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(readOnboarding(IDENTITY, storage)).toBe("skipped");
  });

  it("records completed on Finish and moves focus to the prompt", async () => {
    const user = userEvent.setup();
    const { storage } = renderTour();
    await tourDialog();
    for (let step = 0; step < 3; step += 1) await user.click(button("Next"));
    await user.click(button("Finish"));
    expect(readOnboarding(IDENTITY, storage)).toBe("completed");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(await screen.findByRole("textbox", { name: PROMPT })).toHaveFocus();
  });

  it("moves focus to the page title on Finish when the home has no prompt", async () => {
    const user = userEvent.setup();
    renderTour({ duties: [] });
    await screen.findByRole("dialog");
    await user.click(button("Next"));
    await user.click(button("Finish"));
    expect(screen.getByRole("heading", { level: 1 })).toHaveFocus();
  });

  it("offers a reduced tour with classic navigation and no AI claims without the copilot duty", async () => {
    const user = userEvent.setup();
    renderTour({ duties: [] });
    const dialog = await screen.findByRole("dialog", { name: copy.onboarding.steps.commandBar.title });
    expect(within(dialog).getByText("Step 1 of 2")).toBeInTheDocument();
    await user.click(button("Next"));
    expect(screen.getByRole("dialog", { name: copy.onboarding.steps.classicNav.title })).not.toHaveTextContent(/assistant/i);
    expect(screen.queryByText(copy.onboarding.steps.approvals.title)).not.toBeInTheDocument();
  });

  it("has no axe violations on any step", async () => {
    const user = userEvent.setup();
    renderTour();
    const dialog = await tourDialog();
    for (let step = 0; step < 4; step += 1) {
      expect(pageViolations(dialog.parentElement as HTMLElement)).toEqual([]);
      if (step < 3) await user.click(button("Next"));
    }
  });
});

describe("when the tour must not start by itself", () => {
  it("waits while availability is still being checked", async () => {
    renderTour({ availability: new Promise<Response>(() => undefined) });
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not start away from the home page", async () => {
    renderTour({ path: "/elsewhere" });
    await screen.findByText("Other page");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not start in demo mode", async () => {
    vi.stubEnv("MODE", "demo");
    renderTour({ duties: [] });
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not start when storage cannot persist, but Take the tour still opens it and Skip still closes it", async () => {
    const user = userEvent.setup();
    renderTour({ storage: noPreferenceStorage, duties: [] });
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Help for this page" }));
    await user.click(button("Take the tour"));
    expect(await screen.findByText("Step 1 of 2")).toBeInTheDocument();
    await user.click(button("Skip tour"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("take the tour from help", () => {
  it("reopens at step 1 for a user who completed it, closes the panel and returns focus to the help button", async () => {
    const user = userEvent.setup();
    renderTour({ storage: completedOnboardingStorage(IDENTITY) });
    await screen.findByRole("textbox", { name: PROMPT });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(button("Help for this page"));
    await user.click(button("Take the tour"));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(await tourDialog()).toBeInTheDocument();
    expect(screen.getByText("Step 1 of 4")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(button("Help for this page")).toHaveFocus();
  });
});
