import type { ComponentProps } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AvailabilityState } from "../assistant/availability";
import { CommandPalette, type PaletteEntry } from "../CommandPalette";
import { pushRecent, readFavourites, readRecent, toggleFavourite } from "./recentRecords";
import { isAppPath, searchRecords, type RecordHit, type RecordSearch } from "./records";

const pages: PaletteEntry[] = [{ id: "p:1", group: "Purchasing", label: "Purchase orders", to: "/purchasing/purchase-orders" }];

const po: RecordHit = { type: "purchase_order", id: "po-1", number: "PO-1042", title: "PO-1042", subtitle: "USD 120.00", status: "CONFIRMED", link: "/purchasing/purchase-orders/po-1" };
const vendor: RecordHit = { type: "party", id: "v-1", number: "V-0007", title: "Contoso", subtitle: "organization", status: "active", link: "/party/v-1" };

beforeEach(() => window.localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

function open(search: RecordSearch, identity = "user-a", onSelect = vi.fn(), extra: Partial<ComponentProps<typeof CommandPalette>> = {}) {
  const onClose = vi.fn();
  const announce = vi.fn();
  render(<CommandPalette entries={pages} onSelect={onSelect} onClose={onClose} announce={announce} identity={identity} searchRecords={search} {...extra} />);
  return { onSelect, onClose, announce, user: userEvent.setup() };
}

const ENABLED: AvailabilityState = { status: "enabled", budget: "OK" };

describe("palette record results", () => {
  it("shows records in one Records group with their type and status and opens one with Enter", async () => {
    const search = vi.fn<RecordSearch>().mockResolvedValue([po, vendor]);
    const { user, onSelect } = open(search);
    await user.type(screen.getByRole("combobox"), "contoso");
    const group = await screen.findByRole("group", { name: "Records" });
    expect(within(group).getByRole("option", { name: /Purchase order PO-1042, Confirmed · USD 120.00/ })).toBeInTheDocument();
    expect(within(group).getByRole("option", { name: /Party V-0007 Contoso/ })).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ to: po.link, record: po }));
    expect(readRecent("user-a")).toEqual([po]);
  });

  it("lists production orders and workers with their type in the option name", async () => {
    const order: RecordHit = { type: "production_order", id: "mo-1", number: "MO-24611", title: "MO-24611", status: "REPORTED_AS_FINISHED", link: "/production/mo-1" };
    const worker: RecordHit = { type: "worker", id: "w-1", number: "W-0112", title: "W-0112", subtitle: "employee", link: "/hr/workers/w-1" };
    const { user, onSelect } = open(vi.fn<RecordSearch>().mockResolvedValue([order, worker]));
    await user.type(screen.getByRole("combobox"), "mo-24");
    const records = await screen.findByRole("group", { name: "Records" });
    expect(within(records).getByRole("option", { name: "Production order MO-24611, Reported as finished" })).toBeInTheDocument();
    expect(within(records).getByRole("option", { name: "Worker W-0112, employee" })).toBeInTheDocument();
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ to: "/hr/workers/w-1", record: worker }));
  });

  it("lists page results before the records", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([po]));
    await user.type(screen.getByRole("combobox"), "purchase");
    await screen.findByRole("group", { name: "Records" });
    const labels = screen.getAllByRole("option").map((o) => o.getAttribute("aria-label"));
    expect(labels[0]).toBe("Purchase orders, Purchasing");
    expect(labels[labels.length - 1]).toMatch(/^Purchase order PO-1042/);
  });

  it("does not search for one character and says so when the service fails", async () => {
    const search = vi.fn<RecordSearch>().mockRejectedValue(new Error("down"));
    const { user } = open(search);
    await user.type(screen.getByRole("combobox"), "p");
    expect(search).not.toHaveBeenCalled();
    await user.type(screen.getByRole("combobox"), "u");
    expect(await screen.findByRole("status")).toHaveTextContent(/can't be searched/);
    expect(screen.getByRole("option", { name: "Purchase orders, Purchasing" })).toBeInTheDocument();
  });

  it("lists favourites, then recent records, when the box is empty", () => {
    pushRecent("user-a", po);
    pushRecent("user-a", vendor);
    toggleFavourite("user-a", vendor);
    open(vi.fn<RecordSearch>());
    expect(within(screen.getByRole("group", { name: "Favourite records" })).getByRole("option", { name: /V-0007/ })).toBeInTheDocument();
    const recent = screen.getByRole("group", { name: "Recent records" });
    expect(within(recent).getAllByRole("option")).toHaveLength(1);
    expect(within(recent).getByRole("option", { name: /PO-1042/ })).toBeInTheDocument();
  });

  it("favourites the active record with Alt+F and removes it again", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([po]));
    await user.type(screen.getByRole("combobox"), "po-10");
    await screen.findByRole("group", { name: "Records" });
    await user.keyboard("{Alt>}f{/Alt}");
    expect(readFavourites("user-a")).toEqual([po]);
    expect(screen.getByRole("option", { name: /PO-1042.*Favourite/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove PO-1042 from favourites" }));
    await waitFor(() => expect(readFavourites("user-a")).toEqual([]));
  });

  it("keeps recent records per user", () => {
    pushRecent("user-a", po);
    expect(readRecent("user-b")).toEqual([]);
    pushRecent("", po);
    expect(readRecent("")).toEqual([]);
  });

  it("keeps the newest eight recent records without duplicates", () => {
    for (let i = 0; i < 10; i++) pushRecent("user-a", { ...po, id: `po-${i}`, number: `PO-${i}` });
    pushRecent("user-a", { ...po, id: "po-5", number: "PO-5" });
    const recent = readRecent("user-a");
    expect(recent).toHaveLength(8);
    expect(recent[0].id).toBe("po-5");
    expect(recent.filter((r) => r.id === "po-5")).toHaveLength(1);
  });
});

describe("duty-gated records", () => {
  it("renders exactly the records the service returned and issues no other lookup", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ results: [{ type: "party", id: "v-1", number: "V-0007", title: "Contoso", link: "/party/v-1" }] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const { user } = open(searchRecords);
    await user.type(screen.getByRole("combobox"), "contoso");
    const records = await screen.findByRole("group", { name: "Records" });
    expect(within(records).getAllByRole("option")).toHaveLength(1);
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(["/api/v1/search?q=contoso&limit=20"]);
  });

  it("shows no Records group when the service refuses the caller", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 403 })));
    const { user } = open(searchRecords);
    await user.type(screen.getByRole("combobox"), "purchase");
    expect(await screen.findByRole("status")).toHaveTextContent(/can't be searched/);
    expect(screen.queryByRole("group", { name: "Records" })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Navigate" })).toBeInTheDocument();
  });
});

describe("assistant group", () => {
  it("keeps the Ask the assistant option last when nothing matches and sends the text on Enter", async () => {
    const onAsk = vi.fn();
    const { user, onClose } = open(vi.fn<RecordSearch>().mockResolvedValue([]), "user-a", vi.fn(), { onAsk, assistantState: ENABLED });
    await user.type(screen.getByRole("combobox"), "zzqq");
    const group = screen.getByRole("group", { name: "Assistant" });
    expect(within(group).getByRole("option", { name: /Ask the assistant: zzqq/ })).toBeInTheDocument();
    expect(screen.getByRole("combobox")).toHaveAttribute("aria-expanded", "true");
    await user.keyboard("{Enter}");
    expect(onAsk).toHaveBeenCalledExactlyOnceWith("zzqq");
    expect(onClose).toHaveBeenCalled();
  });

  it("shows the group in Navigate, Records, Assistant order", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([po]), "user-a", vi.fn(), { onAsk: vi.fn(), assistantState: ENABLED });
    await user.type(screen.getByRole("combobox"), "purchase");
    await screen.findByRole("group", { name: "Records" });
    expect(screen.getAllByRole("group").map((g) => g.getAttribute("aria-labelledby") && document.getElementById(g.getAttribute("aria-labelledby") ?? "")?.textContent)).toEqual(["Navigate", "Records", "Assistant"]);
  });

  it("has no group without onAsk or an empty query", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([]), "user-a", vi.fn(), { assistantState: ENABLED });
    await user.type(screen.getByRole("combobox"), "purchase");
    expect(screen.queryByRole("group", { name: "Assistant" })).not.toBeInTheDocument();
  });

  it("shows a disabled row while availability is being checked", async () => {
    const onAsk = vi.fn();
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([]), "user-a", vi.fn(), { onAsk, assistantState: { status: "checking" } });
    await user.type(screen.getByRole("combobox"), "zzqq");
    expect(screen.getByRole("option", { name: "Checking assistant availability" })).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Enter}");
    expect(onAsk).not.toHaveBeenCalled();
  });

  it.each<[AvailabilityState, RegExp]>([
    [{ status: "unavailable", reason: "paused" }, /assistant is paused/],
    [{ status: "unavailable", reason: "off" }, /turned off/],
    [{ status: "unavailable", reason: "noAccess" }, /don't have access/],
    [{ status: "degraded", reason: "budget" }, /usage limit/],
    [{ status: "degraded", reason: "unreachable" }, /can't be reached/],
  ])("replaces the group with one message for %j and keeps pages and records working", async (state, message) => {
    const onAsk = vi.fn();
    const { user, onSelect } = open(vi.fn<RecordSearch>().mockResolvedValue([po]), "user-a", vi.fn(), { onAsk, assistantState: state });
    await user.type(screen.getByRole("combobox"), "purchase");
    expect(screen.getByText(message)).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Assistant" })).not.toBeInTheDocument();
    await screen.findByRole("group", { name: "Records" });
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalled();
    expect(onAsk).not.toHaveBeenCalled();
  });
});

describe("palette keyboard and announcements", () => {
  it("moves the selection to the first and last option with Home and End but not with Ctrl", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([]));
    const input = screen.getByRole("combobox");
    const options = screen.getAllByRole("option");
    await user.click(input);
    await user.keyboard("{End}");
    expect(input).toHaveAttribute("aria-activedescendant", options[options.length - 1].id);
    await user.keyboard("{Control>}{Home}{/Control}");
    expect(input).toHaveAttribute("aria-activedescendant", options[options.length - 1].id);
    await user.keyboard("{Home}");
    expect(input).toHaveAttribute("aria-activedescendant", options[0].id);
  });

  it("announces the result count after a pause and leaves the assistant row out of it", async () => {
    const { user, announce } = open(vi.fn<RecordSearch>().mockResolvedValue([]), "user-a", vi.fn(), { onAsk: vi.fn(), assistantState: ENABLED });
    await user.type(screen.getByRole("combobox"), "purchase");
    await waitFor(() => expect(announce).toHaveBeenCalledWith("1 result"));
  });

  it("caps Navigate at five matches while searching", async () => {
    const many: PaletteEntry[] = Array.from({ length: 8 }, (_, i) => ({ id: `n:${i}`, group: "Admin", label: `Setup ${i}`, to: `/setup/${i}` }));
    render(<CommandPalette entries={many} onSelect={vi.fn()} onClose={vi.fn()} announce={vi.fn()} searchRecords={vi.fn<RecordSearch>().mockResolvedValue([])} />);
    await userEvent.setup().type(screen.getByRole("combobox"), "setup");
    expect(within(screen.getByRole("group", { name: "Navigate" })).getAllByRole("option")).toHaveLength(5);
  });
});

describe("searchRecords", () => {
  it("calls the search service and keeps only known types with in-app links", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          results: [
            { type: "receipt", id: "r1", number: "RC-1", title: "RC-1", link: "/receiving/receipts/r1" },
            { type: "mystery", id: "x", number: "X", title: "X", link: "/x" },
            { type: "party", id: "p", number: "P", title: "P", link: "https://evil.example/p" },
          ],
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const found = await searchRecords("rc-1", new AbortController().signal);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/search?q=rc-1&limit=20");
    expect(found.map((r) => r.id)).toEqual(["r1"]);
  });

  it("rejects when the service answers with an error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 403 })));
    await expect(searchRecords("abc", new AbortController().signal)).rejects.toThrow(/403/);
  });

  it("follows in-app paths only", () => {
    expect(isAppPath("/party/1")).toBe(true);
    expect(isAppPath("//evil.example")).toBe(false);
    expect(isAppPath("https://evil.example")).toBe(false);
  });
});
