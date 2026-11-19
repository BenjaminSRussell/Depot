import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { pageViolations } from "../testing/fakeServer";
import { DataMarking, showsMarking } from "./DataMarking";

const sources = import.meta.glob<string>(["./DataMarking.tsx", "../useDesignation.ts"], { query: "?raw", import: "default", eager: true });

describe("DataMarking", () => {
  it("shows a CUI banner with the designation source as text", () => {
    const { container } = render(<DataMarking marking={{ kind: "cui", source: "RECORD" }} variant="banner" />);
    expect(screen.getByText("CUI")).toBeInTheDocument();
    expect(screen.getByText("Designated at project level")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it.each([
    ["LEGAL_ENTITY", "Designated at legal entity level"],
    ["TENANT", "Designated for the whole organization"],
    ["PARENT_RECORD", "Inherited from the parent contract"],
    ["DEFAULT", "Handle as CUI"],
  ])("words the %s source", (source, text) => {
    render(<DataMarking marking={{ kind: "cui", source }} variant="banner" recordNoun="contract" />);
    expect(screen.getByText(text)).toBeInTheDocument();
  });

  it("shows the CUI word in a badge and in a cell", () => {
    const { container } = render(
      <>
        <DataMarking marking={{ kind: "cui", source: "RECORD" }} variant="badge" />
        <DataMarking marking={{ kind: "cui", source: "RECORD" }} variant="cell" />
      </>,
    );
    expect(screen.getAllByText("CUI")).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says None only in a cell, and renders nothing for a record that is not CUI elsewhere", () => {
    const none = render(<DataMarking marking={{ kind: "none" }} variant="cell" />);
    expect(none.container).toHaveTextContent("None");
    const other = render(
      <>
        <DataMarking marking={{ kind: "none" }} variant="badge" />
        <DataMarking marking={{ kind: "none" }} variant="banner" />
        <DataMarking marking={{ kind: "pending" }} variant="cell" />
      </>,
    );
    expect(other.container).toBeEmptyDOMElement();
  });

  it("shows the neutral unavailable text in a banner or cell, never an alert, and nothing in a badge", () => {
    const { container } = render(
      <>
        <DataMarking marking={{ kind: "unavailable" }} variant="banner" />
        <DataMarking marking={{ kind: "unavailable" }} variant="cell" />
        <DataMarking marking={{ kind: "unavailable" }} variant="badge" />
      </>,
    );
    expect(screen.getAllByText("Designation unavailable")).toHaveLength(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("decides which markings need a header line", () => {
    expect(showsMarking({ kind: "cui", source: "" })).toBe(true);
    expect(showsMarking({ kind: "unavailable" })).toBe(true);
    expect(showsMarking({ kind: "none" })).toBe(false);
    expect(showsMarking({ kind: "pending" })).toBe(false);
  });

  it("cannot be hidden by a user setting: neither source reads preferences or personalization", () => {
    expect(Object.keys(sources)).toHaveLength(2);
    for (const source of Object.values(sources)) expect(source).not.toMatch(/preferenceStorage|personalization/);
  });
});
