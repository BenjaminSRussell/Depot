import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../a11y";
import { EntityMultiLookup } from "./EntityMultiLookup";
import { clearResolvedOptions, setLookupFetcher } from "./entities";

const GASKET = "11111111-1111-4111-8111-111111111111";
const SEAL = "22222222-2222-4222-8222-222222222222";

const products = [
  { id: GASKET, name: "Gasket", productNumber: "A-100" },
  { id: SEAL, name: "Seal ring", productNumber: "B-200" },
];

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

let respond: (url: URL) => Response;

beforeEach(() => {
  setLookupFetcher(null);
  clearResolvedOptions();
  respond = (url) => {
    if (url.pathname === "/api/v1/product/products:search") {
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      return json({ items: products.filter((p) => p.name.toLowerCase().includes(q) || p.productNumber.toLowerCase().includes(q)) });
    }
    const found = products.find((p) => url.pathname === `/api/v1/product/products/${p.id}`);
    return found ? json(found) : json({}, 404);
  };
  vi.stubGlobal("fetch", vi.fn(async (input: string) => respond(new URL(input, "http://localhost"))));
});

afterEach(() => vi.unstubAllGlobals());

function Harness({ initial = [], cap }: { initial?: string[]; cap?: number }) {
  const [ids, setIds] = useState<string[]>(initial);
  return (
    <>
      <label htmlFor="items">Items</label>
      <EntityMultiLookup id="items" entity="product" value={ids} onChange={setIds} cap={cap} />
      <output data-testid="ids">{ids.join(",")}</output>
    </>
  );
}

const paste = (text: string) => fireEvent.paste(screen.getByLabelText("Items"), { clipboardData: { getData: () => text } });

describe("EntityMultiLookup", () => {
  it("turns pasted item numbers into named chips, in order and without duplicates", async () => {
    render(<Harness />);
    paste("B-200, a-100\nB-200");
    expect(await screen.findByText("Seal ring (B-200)")).toBeInTheDocument();
    expect(screen.getByText("Gasket (A-100)")).toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent(`${SEAL},${GASKET}`);
    expect(screen.queryByText(SEAL)).toBeNull();
  });

  it("accepts a pasted id and lists numbers that match nothing", async () => {
    render(<Harness />);
    paste(`${GASKET} Z-999`);
    expect(await screen.findByText("Not found: Z-999")).toBeInTheDocument();
    expect(screen.getByText("Gasket (A-100)")).toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent(GASKET);
  });

  it("does not take a partial number as a match", async () => {
    render(<Harness />);
    paste("A-10 B-200");
    expect(await screen.findByText("Not found: A-10")).toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent(SEAL);
  });

  it("refuses a paste of more numbers than the cap", async () => {
    render(<Harness cap={1} />);
    paste("A-100 B-200");
    expect(await screen.findByText("Add at most 1 products.")).toBeInTheDocument();
    expect(screen.getByTestId("ids")).toBeEmptyDOMElement();
  });

  it("searches by name when typing and picks with the keyboard", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(screen.getByLabelText("Items"), "seal");
    expect(await screen.findByRole("option", { name: /Seal ring/ })).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("ids")).toHaveTextContent(SEAL);
    expect(screen.getByLabelText("Items")).toHaveValue("");
  });

  it("removes a chip and keeps the others", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[GASKET, SEAL]} />);
    await user.click(await screen.findByRole("button", { name: "Remove Gasket (A-100)" }));
    expect(screen.getByTestId("ids")).toHaveTextContent(SEAL);
    expect(screen.queryByText("Gasket (A-100)")).toBeNull();
  });

  it("keeps the chips and says so when the search fails", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[GASKET]} />);
    await screen.findByText("Gasket (A-100)");
    respond = () => json({}, 500);
    await user.type(screen.getByLabelText("Items"), "seal");
    await waitFor(() => expect(screen.getAllByText(/Could not load product matches/).length).toBeGreaterThan(0));
    expect(screen.getByText("Gasket (A-100)")).toBeInTheDocument();
  });

  it("treats a value that is not a list as empty", () => {
    render(<EntityMultiLookup entity="product" id="items" value={undefined as unknown as string[]} onChange={() => undefined} />);
    expect(screen.getByRole("combobox")).toHaveValue("");
  });

  it("has no accessibility violations with chips and an open list", async () => {
    const user = userEvent.setup();
    const { container } = render(<Harness initial={[GASKET]} />);
    await screen.findByText("Gasket (A-100)");
    await user.type(screen.getByLabelText("Items"), "seal");
    await screen.findByRole("option", { name: /Seal ring/ });
    const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });
});
