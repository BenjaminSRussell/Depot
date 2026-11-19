import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { MemoryRouter } from "react-router-dom";
import { ContextSheet } from "./ContextSheet";
import { TopBar } from "./TopBar";

function renderSheet() {
  return render(
    <MemoryRouter>
      <ContextSheet showContext={false} compact={false} onToggleCompact={() => undefined} />
    </MemoryRouter>,
  );
}

function renderTopBar() {
  return render(
    <MemoryRouter>
      <TopBar
        page={{ title: "Orders" }}
        titleRef={createRef<HTMLHeadingElement>()}
        menuRef={createRef<HTMLButtonElement>()}
        drawerOpen={false}
        compact={false}
        onOpenDrawer={() => undefined}
        onToggleCompact={() => undefined}
      />
    </MemoryRouter>,
  );
}

function matchPhone(matches: boolean) {
  window.matchMedia = ((query: string) => ({ matches, media: query, addEventListener: () => undefined, removeEventListener: () => undefined })) as unknown as typeof window.matchMedia;
}

afterEach(() => {
  Reflect.deleteProperty(window, "matchMedia");
});

describe("context sheet", () => {
  it("opens as a labelled dialog holding the help, density and account controls", async () => {
    const user = userEvent.setup();
    renderSheet();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Context" }));
    const sheet = screen.getByRole("dialog", { name: "Context and account" });
    expect(sheet).toHaveAttribute("aria-modal", "true");
    expect(screen.getByRole("button", { name: "Help for this page" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Compact view" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
  });

  it("closes on Escape and returns focus to the opener", async () => {
    const user = userEvent.setup();
    renderSheet();
    const opener = screen.getByRole("button", { name: "Context" });
    await user.click(opener);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("closes from the Close button", async () => {
    const user = userEvent.setup();
    renderSheet();
    await user.click(screen.getByRole("button", { name: "Context" }));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps Tab inside the sheet, wrapping from the last control to the first", async () => {
    const user = userEvent.setup();
    renderSheet();
    await user.click(screen.getByRole("button", { name: "Context" }));
    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Compact view" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
  });
});

describe("top bar on a phone", () => {
  it("shows the Context button instead of the actions when the screen is narrow", () => {
    matchPhone(true);
    renderTopBar();
    expect(screen.getByRole("button", { name: "Context" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Help for this page" })).not.toBeInTheDocument();
  });

  it("keeps the wide top bar on a wide screen", () => {
    matchPhone(false);
    renderTopBar();
    expect(screen.queryByRole("button", { name: "Context" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Help for this page" })).toBeInTheDocument();
  });

  it("falls back to the wide top bar when the browser has no matchMedia", () => {
    renderTopBar();
    expect(screen.queryByRole("button", { name: "Context" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Compact view" })).toBeInTheDocument();
  });
});
