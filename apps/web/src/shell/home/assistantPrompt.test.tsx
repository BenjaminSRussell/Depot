import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { copy } from "../copy";
import type { RouterOutcome } from "../assistant/router";
import { pageViolations } from "../testing/fakeServer";
import { AssistantPrompt } from "./AssistantPrompt";

const prompt = () => screen.getByRole("textbox", { name: copy.home.assistantLabel });

describe("AssistantPrompt", () => {
  it("is labelled, takes focus when nothing else holds it, and has no axe violations", () => {
    const { container } = render(<AssistantPrompt onSubmit={async () => {}} />);
    expect(prompt()).toHaveFocus();
    expect(pageViolations(container)).toEqual([]);
  });

  it("leaves focus alone when another element already holds it", () => {
    const held = document.createElement("button");
    document.body.append(held);
    held.focus();
    render(<AssistantPrompt onSubmit={async () => {}} />);
    expect(held).toHaveFocus();
    held.remove();
  });

  it("shows a field error and sends nothing for empty or blank text", async () => {
    const onSubmit = vi.fn(async () => {});
    const user = userEvent.setup();
    render(<AssistantPrompt onSubmit={onSubmit} />);
    await user.click(screen.getByRole("button", { name: copy.home.assistantSend }));
    expect(screen.getByRole("alert")).toHaveTextContent(copy.home.assistantEmpty);
    expect(prompt()).toHaveAttribute("aria-invalid", "true");
    await user.type(prompt(), "   {Enter}");
    expect(screen.getByRole("alert")).toHaveTextContent(copy.home.assistantEmpty);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("hands the trimmed text over on Enter and on Send", async () => {
    const onSubmit = vi.fn(async () => {});
    const user = userEvent.setup();
    render(<AssistantPrompt onSubmit={onSubmit} />);
    await user.type(prompt(), "  show open invoices {Enter}");
    expect(onSubmit).toHaveBeenLastCalledWith("show open invoices", undefined);
    await user.click(screen.getByRole("button", { name: copy.home.assistantSend }));
    expect(onSubmit).toHaveBeenCalledTimes(2);
  });

  it("announces loading in a polite status region and ignores a second submit while pending", async () => {
    let finish: () => void = () => {};
    const onSubmit = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
    const user = userEvent.setup();
    render(<AssistantPrompt onSubmit={onSubmit} />);
    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-live", "polite");
    expect(status).toBeEmptyDOMElement();
    await user.type(prompt(), "hello{Enter}");
    expect(status).toHaveTextContent(copy.home.assistantLoading);
    await user.type(prompt(), "{Enter}");
    expect(onSubmit).toHaveBeenCalledTimes(1);
    finish();
    await vi.waitFor(() => expect(status).toBeEmptyDOMElement());
  });

  it("shows an error and stays editable when the hand-off fails", async () => {
    const user = userEvent.setup();
    render(<AssistantPrompt onSubmit={async () => Promise.reject(new Error("boom"))} />);
    await user.type(prompt(), "hello{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent(copy.home.assistantFailed);
    expect(prompt()).toBeEnabled();
    expect(prompt()).toHaveValue("hello");
  });

  describe("mode selector and clarify chooser", () => {
    const modeRadio = (name: string) => screen.getByRole("radio", { name });

    it("groups Auto, Ask, Go to and Do, defaults to Auto and disables Do without the duty", () => {
      const { container } = render(<AssistantPrompt onSubmit={async () => {}} />);
      expect(screen.getByRole("group", { name: copy.home.assistantModeLegend })).toBeInTheDocument();
      expect(modeRadio("Auto")).toBeChecked();
      expect(modeRadio("Do")).toBeDisabled();
      expect(modeRadio("Ask")).toBeEnabled();
      expect(pageViolations(container)).toEqual([]);
    });

    it("sends the chosen mode with the text and enables Do with the duty", async () => {
      const onSubmit = vi.fn(async (_text: string, _mode?: string) => {});
      const user = userEvent.setup();
      render(<AssistantPrompt onSubmit={onSubmit} canAct />);
      await user.click(modeRadio("Do"));
      await user.type(prompt(), "cancel this planned order{Enter}");
      expect(onSubmit).toHaveBeenLastCalledWith("cancel this planned order", "act");
    });

    it("offers the two clarify options and resends the text with the one picked", async () => {
      const onSubmit = vi
        .fn(async (_text: string, _mode?: string): Promise<RouterOutcome> => ({ status: "dispatched", kind: "ask", handled: "own" }))
        .mockResolvedValueOnce({ status: "clarify", options: ["navigate", "ask"] });
      const user = userEvent.setup();
      const { container } = render(<AssistantPrompt onSubmit={onSubmit} />);
      await user.type(prompt(), "invoices{Enter}");
      expect(await screen.findByRole("button", { name: copy.home.assistantChoices.navigate })).toBeInTheDocument();
      expect(pageViolations(container)).toEqual([]);
      await user.click(screen.getByRole("button", { name: copy.home.assistantChoices.ask }));
      expect(onSubmit).toHaveBeenLastCalledWith("invoices", "ask");
      await vi.waitFor(() => expect(screen.queryByRole("button", { name: copy.home.assistantChoices.navigate })).not.toBeInTheDocument());
    });
  });

  describe("router outcomes", () => {
    const sendWith = async (outcome: RouterOutcome) => {
      const onSubmit = vi.fn(async (_text: string, _mode?: string) => outcome);
      const user = userEvent.setup();
      const view = render(<AssistantPrompt onSubmit={onSubmit} />);
      await user.type(prompt(), "cancel this planned order{Enter}");
      return { onSubmit, user, view };
    };

    it("explains a blocked action in the polite status region and offers to go there instead", async () => {
      const { onSubmit, user, view } = await sendWith({ status: "blocked", reason: "no-duty", alternative: "navigate" });
      await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(copy.home.assistantBlocked));
      expect(pageViolations(view.container)).toEqual([]);
      await user.click(screen.getByRole("button", { name: copy.home.assistantBlockedAction }));
      expect(onSubmit).toHaveBeenLastCalledWith("cancel this planned order", "navigate");
    });

    it("shows the server's code and message for a refusal", async () => {
      await sendWith({ status: "refused", code: "AGT-0202", message: "Usage limit reached." });
      expect(await screen.findByRole("alert")).toHaveTextContent("Usage limit reached. (AGT-0202)");
    });

    it("announces a search note and a clarify question", async () => {
      await sendWith({ status: "search", reason: "error" });
      await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(copy.home.assistantSearchNote));
      cleanup();
      await sendWith({ status: "clarify", options: ["navigate", "ask"] });
      await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(copy.home.assistantClarify));
    });
  });
});
