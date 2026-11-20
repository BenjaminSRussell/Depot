import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import apiSource from "../../../../../services/search/internal/api/api.go?raw";
import storeSource from "../../../../../services/search/internal/store/store.go?raw";
import type { DepotModule } from "../../modules/registry";
import { resolveOption } from "../lookup/entities";
import { Shell } from "../Shell";
import { pageViolations, useFakeServer } from "../testing/fakeServer";
import { completedOnboardingStorage } from "../testing/onboardingFixtures";
import { HomePage } from "../pages/HomePage";
import { addPin, MAX_PINS, readPins, type PinTarget } from "./pinStore";
import { RECORD_ROUTES } from "./pinTargets";

vi.mock("../lookup/entities", async (importOriginal) => ({ ...(await importOriginal<typeof import("../lookup/entities")>()), resolveOption: vi.fn() }));

useFakeServer();

const PAGE = "/ledger/journal-batches";
const PAGE_LABEL = "Post a journal";
const GUARDED_PAGE = "/ledger/open-items";
const ORDER_ROUTE = "/purchasing/purchase-orders/:id";

const modules: DepotModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [],
    nav: [
      { label: "Journal batches", to: PAGE },
      { label: "Open items", to: GUARDED_PAGE, duty: "ledger.open-items" },
    ],
  },
  {
    id: "purchasing",
    title: "Purchasing",
    routes: [
      { path: ORDER_ROUTE, handle: { duty: "po.read" }, element: <p>Purchase order page</p> },
      { path: "/production/:id", element: <p>Production order page</p> },
    ],
    nav: [{ label: "Purchase orders", to: "/purchasing/purchase-orders" }],
  },
];

const page = (route: string): PinTarget => ({ kind: "page", route });
const order = (id: string): PinTarget => ({ kind: "record", recordType: "purchase_order", id });
const production = (id: string): PinTarget => ({ kind: "record", recordType: "production_order", id });

function seed(identity: string, ...targets: PinTarget[]) {
  for (const target of targets) addPin(identity, target);
}

function renderShell(path: string, { identity = "ann", duties = ["po.read", "ledger.open-items"] }: { identity?: string; duties?: string[] } = {}) {
  const home = <Link to="/">Back to the home</Link>;
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<Shell modules={modules} duties={duties} identity={identity} features={{}} onboardingStorage={completedOnboardingStorage(identity)} />}>
          <Route path="/" element={<HomePage />} />
          <Route path={PAGE} element={home} />
          <Route path={GUARDED_PAGE} element={home} />
          <Route path={ORDER_ROUTE} element={home} />
          <Route path="/production/:id" element={home} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const region = () => screen.getByRole("region", { name: "Pinned" });
const pinnedLinks = () => within(region()).queryAllByRole("link");
const status = () => screen.getByRole("status");

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(resolveOption).mockReset();
  vi.mocked(resolveOption).mockImplementation(async (_entity, id) => ({ id, name: `Order ${id}` }));
});
afterEach(() => {
  vi.restoreAllMocks();
  cleanup();
});

describe("Pin to home", () => {
  it("pins the current page, toggles to Unpin with aria-pressed, and unpins again", async () => {
    const user = userEvent.setup();
    renderShell(PAGE);
    const toggle = screen.getByRole("button", { name: "Pin to home" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Unpin" })).toHaveAttribute("aria-pressed", "true");
    expect(status()).toHaveTextContent(`Pinned ${PAGE_LABEL} to home.`);
    expect(readPins("ann")).toMatchObject([{ kind: "page", route: PAGE }]);
    await user.click(screen.getByRole("button", { name: "Unpin" }));
    expect(screen.getByRole("button", { name: "Pin to home" })).toHaveAttribute("aria-pressed", "false");
    expect(readPins("ann")).toEqual([]);
  });

  it("pins a record by its type and id only, and shows it on the home at once", async () => {
    const user = userEvent.setup();
    renderShell("/purchasing/purchase-orders/po-77");
    await user.click(screen.getByRole("button", { name: "Pin to home" }));
    expect(readPins("ann")).toMatchObject([{ kind: "record", recordType: "purchase_order", id: "po-77" }]);
    expect(window.localStorage.getItem("depot.pins.v1.ann")).not.toContain("Order po-77");
    await user.click(screen.getByRole("link", { name: "Back to the home" }));
    expect(await within(region()).findByRole("link", { name: "Order po-77" })).toHaveAttribute("href", "/purchasing/purchase-orders/po-77");
  });

  it("offers no toggle on a page that is neither a nav entry nor a record", () => {
    renderShell("/");
    expect(screen.queryByRole("button", { name: /^(Pin to home|Unpin)$/ })).toBeNull();
  });

  it("offers no toggle on a create page that looks like a record id", () => {
    renderShell("/production/new");
    expect(screen.queryByRole("button", { name: "Pin to home" })).toBeNull();
  });

  it("tells the user about the limit and stores nothing more", async () => {
    seed("ann", ...Array.from({ length: MAX_PINS }, (_, n) => production(`MO-${n}`)));
    const user = userEvent.setup();
    renderShell(PAGE);
    await user.click(screen.getByRole("button", { name: "Pin to home" }));
    expect(status()).toHaveTextContent(`You can pin up to ${MAX_PINS} items. Remove one to pin another.`);
    expect(screen.getByRole("button", { name: "Pin to home" })).toHaveAttribute("aria-pressed", "false");
    expect(readPins("ann")).toHaveLength(MAX_PINS);
  });
});

describe("Pinned region", () => {
  it("lists pins in order, moves them with the keyboard, removes one and announces each change", async () => {
    seed("ann", page(PAGE), order("po-1"), production("MO-1"));
    const user = userEvent.setup();
    renderShell("/");
    await within(region()).findByRole("link", { name: "Order po-1" });
    expect(pinnedLinks().map((l) => l.textContent)).toEqual([PAGE_LABEL, "Order po-1", "Production orders"]);

    screen.getByRole("button", { name: `Move down, ${PAGE_LABEL}` }).focus();
    await user.keyboard("{Enter}");
    expect(pinnedLinks().map((l) => l.textContent)).toEqual(["Order po-1", PAGE_LABEL, "Production orders"]);
    expect(status()).toHaveTextContent(`Moved ${PAGE_LABEL} down, position 2 of 3.`);
    expect(screen.getByRole("button", { name: `Move down, ${PAGE_LABEL}` })).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Move up, Production orders" }));
    expect(pinnedLinks().map((l) => l.textContent)).toEqual(["Order po-1", "Production orders", PAGE_LABEL]);

    screen.getByRole("button", { name: "Remove Order po-1" }).focus();
    await user.keyboard("{Enter}");
    expect(pinnedLinks().map((l) => l.textContent)).toEqual(["Production orders", PAGE_LABEL]);
    expect(status()).toHaveTextContent("Removed Order po-1 from Pinned.");
    expect(region()).toHaveFocus();
    expect(readPins("ann").map((p) => (p.kind === "page" ? p.route : p.id))).toEqual(["MO-1", PAGE]);
  });

  it("does nothing when Move up is pressed on the first pin", async () => {
    seed("ann", page(PAGE), production("MO-1"));
    const user = userEvent.setup();
    renderShell("/");
    await within(region()).findByRole("link", { name: "Production orders" });
    await user.click(screen.getByRole("button", { name: `Move up, ${PAGE_LABEL}` }));
    expect(pinnedLinks().map((l) => l.textContent)).toEqual([PAGE_LABEL, "Production orders"]);
  });

  it("shows the same pins in the same order after the page is loaded again", async () => {
    seed("ann", production("MO-2"), page(PAGE), production("MO-1"));
    renderShell("/");
    await within(region()).findByRole("link", { name: PAGE_LABEL });
    const first = pinnedLinks().map((l) => l.getAttribute("href"));
    cleanup();
    renderShell("/");
    await within(region()).findByRole("link", { name: PAGE_LABEL });
    expect(pinnedLinks().map((l) => l.getAttribute("href"))).toEqual(first);
    expect(first).toEqual(["/production/MO-2", PAGE, "/production/MO-1"]);
  });

  it("shows each person only their own pins", async () => {
    seed("ann", page(PAGE));
    seed("bob", production("MO-9"));
    renderShell("/", { identity: "bob" });
    await within(region()).findByRole("link", { name: "Production orders" });
    expect(pinnedLinks()).toHaveLength(1);
    expect(within(region()).queryByRole("link", { name: PAGE_LABEL })).toBeNull();
  });

  it("hides a pin for a page or record the person can no longer open, without asking for its details", async () => {
    seed("ann", page(GUARDED_PAGE), order("po-1"), production("MO-1"));
    renderShell("/", { duties: [] });
    await within(region()).findByRole("link", { name: "Production orders" });
    expect(pinnedLinks()).toHaveLength(1);
    expect(screen.queryByText(/po-1/)).toBeNull();
    expect(resolveOption).not.toHaveBeenCalled();
  });

  it("hides a record whose lookup finds nothing", async () => {
    seed("ann", order("gone"), production("MO-1"));
    vi.mocked(resolveOption).mockResolvedValue(null);
    renderShell("/");
    await within(region()).findByRole("link", { name: "Production orders" });
    expect(pinnedLinks()).toHaveLength(1);
    expect(resolveOption).toHaveBeenCalledWith("purchaseOrder", "gone");
  });

  it("shows an empty message when nothing is pinned", () => {
    renderShell("/");
    expect(within(region()).getByText(/Pin a page or a record/)).toBeInTheDocument();
  });

  it("has no axe violations and keeps every control reachable by keyboard", async () => {
    seed("ann", page(PAGE), order("po-1"));
    const user = userEvent.setup();
    renderShell("/");
    await within(region()).findByRole("link", { name: "Order po-1" });
    expect(pageViolations(region())).toEqual([]);
    const buttons = within(region()).getAllByRole("button");
    expect(buttons).toHaveLength(6);
    for (const button of buttons) expect(button).toHaveProperty("tabIndex", 0);
    await user.tab();
    expect(document.body).not.toHaveFocus();
  });
});

describe("storage unavailable", () => {
  it("hides the toggle and the region and raises no error", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    renderShell(PAGE);
    expect(screen.queryByRole("button", { name: /^(Pin to home|Unpin)$/ })).toBeNull();
    cleanup();
    renderShell("/");
    expect(screen.queryByRole("region", { name: "Pinned" })).toBeNull();
  });

  it("hides the controls when no one is signed in", () => {
    renderShell(PAGE, { identity: "" });
    expect(screen.queryByRole("button", { name: "Pin to home" })).toBeNull();
  });
});

describe("record links", () => {
  it("match the links the search service returns", () => {
    const typeNames = new Map([...storeSource.matchAll(/^\s*(Type\w+)\s*=\s*"(\w+)"/gm)].map((m) => [m[1], m[2]]));
    const sources = [...apiSource.matchAll(/\{store\.(Type\w+), \[\]string\{[^}]*\}, "([^"]+)"/g)].map((m) => [typeNames.get(m[1] ?? "") ?? m[1], m[2]]);
    expect(Object.fromEntries(Object.entries(RECORD_ROUTES).map(([type, path]) => [type, path.replace(":id", "%s")]))).toEqual(Object.fromEntries(sources));
  });
});
