import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { pageViolations } from "./a11yTest";
import { AiSuggestion } from "./AiSuggestion";
import { GENERATED_BADGE } from "./domain";
import { suggestionFixtures, UNRENDERED_MARKER } from "./suggestionFixtures";
import type { SuggestionHost, SuggestionViewModel } from "./suggestionModel";

const HOSTS = Object.keys(suggestionFixtures) as SuggestionHost[];
const ALL_DUTIES = ["receiving.ap.inquire", "ledger.journal.read"];

function renderHost(host: SuggestionHost, duties: string[] = []) {
  return render(
    <MemoryRouter>
      <PrincipalProvider duties={duties}>
        <AiSuggestion
          host={host}
          suggestion={suggestionFixtures[host].suggestion}
        >
          <p>Host body</p>
        </AiSuggestion>
      </PrincipalProvider>
    </MemoryRouter>,
  );
}

describe("AiSuggestion", () => {
  it("names no version when the producer has none", async () => {
    const { agentVersion: _omitted, ...withoutVersion } = suggestionFixtures.answer.suggestion;
    render(
      <MemoryRouter>
        <PrincipalProvider duties={[]}>
          <AiSuggestion host="answer" suggestion={{ ...withoutVersion, agentLabel: "Copilot" }} />
        </PrincipalProvider>
      </MemoryRouter>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Why this?" }));
    expect(screen.getByText("Produced by Copilot.")).toBeInTheDocument();
  });

  it.each(HOSTS)(
    "%s: shows the badge and the host body with and without duties",
    (host) => {
      const { unmount } = renderHost(host);
      expect(screen.getByText(GENERATED_BADGE)).toBeInTheDocument();
      expect(screen.getByText("Host body")).toBeInTheDocument();
      unmount();
      renderHost(host, ALL_DUTIES);
      expect(screen.getByText(GENERATED_BADGE)).toBeInTheDocument();
      expect(
        screen.getByRole("group", { name: "AI suggestion" }),
      ).toHaveAttribute("data-host", host);
    },
  );

  it.each(HOSTS)(
    "%s: both disclosures open and close from the keyboard with aria-expanded",
    async (host) => {
      renderHost(host, ALL_DUTIES);
      const user = userEvent.setup();
      for (const name of ["Why this?", "Data used"]) {
        const toggle = screen.getByRole("button", { name });
        expect(toggle).toHaveAttribute("aria-expanded", "false");
        toggle.focus();
        await user.keyboard("{Enter}");
        expect(toggle).toHaveAttribute("aria-expanded", "true");
        await user.keyboard(" ");
        expect(toggle).toHaveAttribute("aria-expanded", "false");
        await user.keyboard("{Enter}");
      }
      expect(pageViolations()).toEqual([]);
    },
  );

  it.each(HOSTS)(
    "%s: never renders prompt text, redacted fields or raw tool names",
    async (host) => {
      renderHost(host, ALL_DUTIES);
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "Why this?" }));
      await user.click(screen.getByRole("button", { name: "Data used" }));
      expect(document.body).not.toHaveTextContent(UNRENDERED_MARKER);
      expect(document.body).not.toHaveTextContent("depot.");
    },
  );

  it("shows rationale, confidence and the agent version when the trace carries them", async () => {
    renderHost("answer");
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Why this?" }));
    expect(
      screen.getByText("Produced by Journal review agent, version 3."),
    ).toBeVisible();
    expect(
      screen.getByText(
        "The posting differs from the usual pattern for this account.",
      ),
    ).toBeVisible();
    expect(screen.getByText("Confidence: 82%")).toBeVisible();
  });

  it("shows the version and the plain fallback when no rationale was recorded", async () => {
    renderHost("proposal");
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Why this?" }));
    expect(
      screen.getByText("Produced by Invoice coding agent, version 2."),
    ).toBeVisible();
    expect(
      screen.getByText("No rationale was recorded for this suggestion."),
    ).toBeVisible();
    expect(screen.queryByText(/Confidence/)).toBeNull();
  });

  it("lists tools and the plain fallback when no source records were recorded", async () => {
    renderHost("feed-item");
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Data used" }));
    expect(
      screen.getByRole("list", { name: "Tools called" }),
    ).toHaveTextContent("Flag on production order");
    expect(
      screen.getByText("No source records were recorded for this suggestion."),
    ).toBeVisible();
  });

  it("links a record only when the user holds its page duty", async () => {
    const { unmount } = renderHost("proposal", ["receiving.ap.inquire"]);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Data used" }));
    expect(
      screen.getByRole("link", { name: "Vendor invoice: Invoice INV-7" }),
    ).toHaveAttribute("href", "/ap-invoices/inv-7");
    unmount();

    renderHost("proposal");
    await user.click(screen.getByRole("button", { name: "Data used" }));
    expect(screen.queryByRole("link")).toBeNull();
    expect(
      screen.getByRole("list", { name: "Records read" }),
    ).toHaveTextContent("Vendor invoice");
    expect(document.body).not.toHaveTextContent("INV-7");
  });

  it("shows an unknown record type as plain text with no id or label", async () => {
    renderHost("briefing-item", ALL_DUTIES);
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Data used" }));
    expect(
      screen.getByRole("list", { name: "Records read" }),
    ).toHaveTextContent("A record");
    expect(
      screen.getByRole("list", { name: "Tools called" }),
    ).toHaveTextContent("A tool call");
    expect(document.body).not.toHaveTextContent("Hidden label");
  });

  function renderMarked(extra: Partial<SuggestionViewModel>) {
    return render(
      <MemoryRouter>
        <PrincipalProvider duties={[]}>
          <AiSuggestion host="answer" suggestion={{ ...suggestionFixtures.answer.suggestion, ...extra }} />
        </PrincipalProvider>
      </MemoryRouter>,
    );
  }

  it.each(["CUI", "UNKNOWN"])("marks an answer with data class %s as CUI next to the AI badge", (dataClass) => {
    renderMarked({ dataClass });
    expect(screen.getByText("CUI")).toBeInTheDocument();
  });

  it.each([undefined, "NONE", "P3"])("shows no CUI marking for data class %s", (dataClass) => {
    renderMarked({ dataClass });
    expect(screen.queryByText("CUI")).not.toBeInTheDocument();
  });

  it("counts left-out records with the reason, never naming them, and skips NEED_TO_KNOW", () => {
    renderMarked({
      excluded: [
        { reason: "TENANT_POLICY", count: 2 },
        { reason: "CUI_ROUTE", count: 1 },
        { reason: "NEED_TO_KNOW", count: 5 },
      ],
    });
    expect(screen.getByText("3 records were left out: tenant policy, CUI route.")).toBeInTheDocument();
  });

  it("shows no notice when only NEED_TO_KNOW records were excluded", () => {
    renderMarked({ excluded: [{ reason: "NEED_TO_KNOW", count: 4 }] });
    expect(screen.queryByText(/left out/)).not.toBeInTheDocument();
  });

  it("labels an unlisted reason as policy and accepts the single-object reply shape", () => {
    renderMarked({ excluded: { reason: "NO_APPROVED_ROUTE", count: 1 } });
    expect(screen.getByText("1 record was left out: policy.")).toBeInTheDocument();
  });
});
