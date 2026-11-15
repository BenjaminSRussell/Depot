import { screen, waitFor } from "@testing-library/react";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { WorkContext } from "../../shell/lookup/workContextState";
import { StatGroup } from "../../shell/ui";
import { parseDso } from "./api/prediction";
import { DsoTile } from "./DsoTile";
import { json, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const forecast = { legalEntityId: LE, dsoDays: 41.3, openInvoices: 25, openAmount: "12500.00", avgPLate30d: "0.2150", scoredAt: "2027-03-30T10:00:00Z", stale: false };

function tile(legalEntityId: string) {
  return render(
    <WorkContext.Provider value={{ identity: "tester", legalEntityId, siteId: "", setLegalEntityId: () => undefined, setSiteId: () => undefined }}>
      <MemoryRouter>
        <StatGroup label="Tiles">
          <DsoTile />
        </StatGroup>
      </MemoryRouter>
    </WorkContext.Provider>,
  );
}

describe("Forecast days to pay tile", () => {
  it("asks for a legal entity before it calls anything", () => {
    tile("");
    expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("Choose a legal entity in the header.");
    expect(server.calls).toHaveLength(0);
  });

  it("shows the forecast of the legal entity with the late share", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/predictions/payment/dso" ? json(200, forecast) : undefined);
    tile(LE);
    await waitFor(() => expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("Forecast days to pay 41.3"));
    expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("25 open invoices; 22% expected over 30 days late.");
    expect(server.calls[0]?.url.searchParams.get("legalEntityId")).toBe(LE);
  });

  it("says so while nothing is scored, when the forecast is old, and when it cannot be loaded", async () => {
    server.handler = () => json(404, { code: "AGT-0404", detail: "no open invoices of this legal entity have been scored yet" });
    const first = tile(LE);
    await waitFor(() => expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("Not scored yet"));
    first.unmount();
    server.handler = () => json(200, { ...forecast, stale: true });
    const second = tile(LE);
    await waitFor(() => expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("more than 7 days old"));
    second.unmount();
    server.handler = () => json(503, { code: "COM-0500", detail: "unavailable" });
    tile(LE);
    await waitFor(() => expect(screen.getByRole("list", { name: "Tiles" })).toHaveTextContent("Could not load the forecast."));
  });

  it("refuses a body of another shape", () => {
    expect(() => parseDso({ ...forecast, dsoDays: "41" })).toThrow("unexpected shape");
    expect(parseDso(forecast).dsoDays).toBe(41.3);
  });
});
