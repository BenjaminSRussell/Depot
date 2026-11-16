import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { idempotencyKey, json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findAlert } from "../../test-utils";
import type { Metric, SavedQuery } from "./api/client";
import { lookbackOf } from "./ExplorerPage";
import analytics from "./module";

useFakeServer();

const Q1 = "0195f0a0-0000-7000-8000-0000000000a1";
const revenue: Metric = {
  metricId: "revenue",
  version: 1,
  name: "Revenue",
  formula: "sum(revenue)",
  grain: "journal_line",
  allowedDimensions: ["legal_entity_id"],
  nonFinancial: false,
  status: "PUBLISHED",
};
const saved: SavedQuery = { id: Q1, name: "Revenue by entity", measures: ["revenue"], dimensions: ["legal_entity_id"], lookbackDays: 90, createdAt: "2026-10-01T00:00:00Z" };

const serve = (extra: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined = () => undefined) => {
  server.handler = (url, init) => {
    if (url.pathname === "/api/v1/analytics/metrics") return json(200, { items: [revenue] });
    return extra(url, init);
  };
  setLookupFetcher(null);
};

describe("saved explorer queries", () => {
  it("counts the days a range covers, within the service's limit", () => {
    expect(lookbackOf({ measures: [], dimensions: [], from: "2026-10-01", to: "2026-10-31" })).toBe(30);
    expect(lookbackOf({ measures: [], dimensions: [], from: "2026-10-31", to: "2026-10-31" })).toBe(1);
    expect(lookbackOf({ measures: [], dimensions: [], from: "2000-01-01", to: "2026-10-31" })).toBe(1826);
  });

  it("saves the current choice under a name with one Idempotency-Key", async () => {
    serve((url, init) => (init.method === "POST" && url.pathname === "/api/v1/analytics/saved-queries" ? json(201, saved) : undefined));
    const { container } = renderRoutes(analytics.routes, "/analytics/explorer?measure=revenue");
    expect(await screen.findByRole("checkbox", { name: "Revenue" })).toBeChecked();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("checkbox", { name: "Legal entity id" }));
    fireEvent.click(screen.getByRole("button", { name: "Save query" }));
    expect(await screen.findByText("Name the query.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Save the current choice as"), { target: { value: " Revenue by entity " } });
    fireEvent.change(screen.getByLabelText("Look-back (days)"), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Save query" }));
    expect(await screen.findByText("Saved Revenue by entity.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).not.toBeNull();
    expect(post.body).toEqual({ name: "Revenue by entity", measures: ["revenue"], dimensions: ["legal_entity_id"], lookbackDays: 90 });
  });

  it("puts a name the user already has on the name field", async () => {
    serve((_url, init) => (init.method === "POST" ? json(409, { code: "ANA-0115", detail: "you already have a saved query with this name" }) : undefined));
    renderRoutes(analytics.routes, "/analytics/explorer?measure=revenue");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.change(screen.getByLabelText("Save the current choice as"), { target: { value: "Revenue by entity" } });
    fireEvent.click(screen.getByRole("button", { name: "Save query" }));
    expect(await screen.findByText("You already have a saved query with this name.")).toBeInTheDocument();
  });

  it("fills the form from a saved query picked by name", async () => {
    serve((url) => (url.pathname === "/api/v1/analytics/saved-queries" ? json(200, { items: [saved] }) : undefined));
    renderRoutes(analytics.routes, "/analytics/explorer");
    await screen.findByRole("checkbox", { name: "Revenue" });
    expect(screen.getByRole("checkbox", { name: "Revenue" })).not.toBeChecked();
    fireEvent.change(screen.getByLabelText("Saved query"), { target: { value: "reve" } });
    fireEvent.click(await screen.findByRole("option", { name: /Revenue by entity/ }));
    await waitFor(() => expect(screen.getByRole("checkbox", { name: "Revenue" })).toBeChecked());
    expect(screen.getByRole("checkbox", { name: "Legal entity id" })).toBeChecked();
    expect(screen.getByLabelText("Look-back (days)")).toHaveValue("90");
    expect(await screen.findByText("Loaded Revenue by entity.")).toBeInTheDocument();
  });

  it("deletes the selected saved query", async () => {
    serve((url, init) => {
      if (init.method === "DELETE") return json(200, {});
      if (url.pathname === "/api/v1/analytics/saved-queries") return json(200, { items: [saved] });
      return undefined;
    });
    renderRoutes(analytics.routes, "/analytics/explorer");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.change(screen.getByLabelText("Saved query"), { target: { value: "reve" } });
    fireEvent.click(await screen.findByRole("option", { name: /Revenue by entity/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete the selected query" }));
    expect(await screen.findByText("Deleted the saved query.")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "DELETE")!.url.pathname).toBe(`/api/v1/analytics/saved-queries/${Q1}`);
    expect(screen.queryByRole("button", { name: "Delete the selected query" })).toBeNull();
  });

  it("shows a refusal when the saved query cannot be loaded", async () => {
    serve((url) => (url.pathname === "/api/v1/analytics/saved-queries" ? json(403, { code: "AUTH-0003", detail: "missing duty analytics.view" }) : undefined));
    renderRoutes(analytics.routes, "/analytics/explorer");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.change(screen.getByLabelText("Saved query"), { target: { value: "0195f0a0-0000-7000-8000-0000000000a1" } });
    expect((await findAlert()).textContent).toContain("missing duty");
  });
});
