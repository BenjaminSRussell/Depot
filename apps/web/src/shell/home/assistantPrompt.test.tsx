import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { copy } from "../copy";
import { pageViolations } from "../testing/fakeServer";
import { AssistantPrompt } from "./AssistantPrompt";
import { routePrompt } from "./assistantRouter";

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
    expect(onSubmit).toHaveBeenLastCalledWith("show open invoices");
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
});

describe("routePrompt", () => {
  it("opens the copilot in ask mode with the text", async () => {
    await expect(routePrompt("why is cash low")).resolves.toEqual({ to: "/copilot", state: { prompt: "why is cash low", mode: "ask" } });
  });
});
