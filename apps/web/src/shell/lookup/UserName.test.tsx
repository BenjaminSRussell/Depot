import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { clearResolvedOptions, setLookupFetcher } from "./entities";
import { UserName } from "./UserName";

beforeEach(() => {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    const ids = new URL(url, "http://localhost").searchParams.get("ids") ?? "";
    const items = ids === "u-1" ? [{ userId: "u-1", displayName: "Ada Lovelace" }] : [];
    return new Response(JSON.stringify({ items }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});
afterEach(() => {
  setLookupFetcher(null);
  clearResolvedOptions();
});

describe("UserName", () => {
  it("shows the user's name inside a paragraph, never the id, and adds no controls", async () => {
    const { container } = render(
      <p>
        Posted by <UserName id="u-1" />.
      </p>,
    );
    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(container.textContent).not.toContain("u-1");
    expect(container.querySelector("details, button")).toBeNull();
  });

  it("says so when the user is no longer listed, and shows a dash or the given text for no id", async () => {
    render(
      <p>
        <UserName id="u-gone" /> | <UserName id="" /> | <UserName id={null} empty="Pending" />
      </p>,
    );
    expect(await screen.findByText("A user who is no longer listed")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText("Pending")).toBeInTheDocument();
  });
});
