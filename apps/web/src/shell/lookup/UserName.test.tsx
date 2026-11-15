import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { clearResolvedOptions, setLookupFetcher } from "./entities";
import { UserName } from "./UserName";

let requests: string[];
let failing: boolean;

beforeEach(() => {
  clearResolvedOptions();
  requests = [];
  failing = false;
  setLookupFetcher(async (url) => {
    requests.push(url);
    if (failing) return new Response("{}", { status: 500 });
    const ids = (new URL(url, "http://localhost").searchParams.get("ids") ?? "").split(",");
    const names: Record<string, string> = { "u-1": "Ada Lovelace", "u-2": "Grace Hopper" };
    const items = ids.filter((id) => names[id]).map((id) => ({ userId: id, displayName: names[id] }));
    return new Response(JSON.stringify({ items }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});
afterEach(() => {
  setLookupFetcher(null);
  clearResolvedOptions();
});

describe("UserName", () => {
  it("shows a neutral placeholder while loading, then the name, never the id, and adds no controls", async () => {
    const { container } = render(
      <p>
        Posted by <UserName id="u-1" />.
      </p>,
    );
    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
    expect(container.textContent).not.toContain("no longer listed");
    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(container.textContent).not.toContain("u-1");
    expect(container.querySelector("details, button")).toBeNull();
  });

  it("says a user is no longer listed only when the server found nothing, and 'Not available' when the lookup failed", async () => {
    const { unmount } = render(
      <p>
        <UserName id="u-gone" /> | <UserName id="" /> | <UserName id={null} empty="Pending" />
      </p>,
    );
    expect(await screen.findByText("A user who is no longer listed")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText("Pending")).toBeInTheDocument();
    unmount();
    clearResolvedOptions();
    failing = true;
    render(<UserName id="u-1" />);
    expect(await screen.findByText("Not available")).toBeInTheDocument();
  });

  it("labels a background actor and asks nothing for it", () => {
    render(
      <p>
        <UserName id="service:intercompany-charges" /> and <UserName id="system:finreport-sweep" />
      </p>,
    );
    expect(screen.getByText("System (intercompany-charges)")).toBeInTheDocument();
    expect(screen.getByText("System (finreport-sweep)")).toBeInTheDocument();
    expect(requests).toEqual([]);
  });

  it("asks for all the names of a page in one request", async () => {
    render(
      <ul>
        <li><UserName id="u-1" /></li>
        <li><UserName id="u-2" /></li>
        <li><UserName id="u-1" /></li>
      </ul>,
    );
    expect(await screen.findByText("Grace Hopper")).toBeInTheDocument();
    expect(requests).toHaveLength(1);
    expect(requests[0]).toContain("ids=u-1%2Cu-2");
  });
});
