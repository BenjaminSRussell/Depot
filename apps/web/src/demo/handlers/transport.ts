// Transport service (/api/v1/transport) for the demo: the carriers' services, their rate cards, the freight bills the
// carriers are checked against, and the loads those bills belong to. Reads, plus adding a carrier service.
import type { CarrierService, FreightBill, Load, RateCard } from "../../modules/transport/api/client";
import { PLANT, VENDORS } from "../company";
import { reply, route } from "../backend";
import { at, day, table, uid } from "../store";

const BASE = "/api/v1/transport";
const K_SERVICE = 912;
const K_BILL = 913;
const K_LOAD = 914;
const K_CARD = 915;

const carrier = (n: number) => VENDORS[n].id;

const services = table<CarrierService>([
  { id: uid(K_SERVICE, 1), carrierId: carrier(4), code: "GROUND", name: "Ground freight", mode: "LTL", active: true },
  { id: uid(K_SERVICE, 2), carrierId: carrier(4), code: "EXPEDITED", name: "Expedited freight", mode: "LTL", active: true },
  { id: uid(K_SERVICE, 3), carrierId: carrier(5), code: "TL-DRY", name: "Full truckload, dry van", mode: "TL", active: true },
  { id: uid(K_SERVICE, 4), carrierId: carrier(5), code: "PARCEL-2D", name: "Two-day parcel", mode: "PARCEL", active: false },
]);

route("GET", `${BASE}/carrier-services`, ({ query }) => {
  const q = (query.get("q") ?? "").trim().toLowerCase();
  const rows = services
    .all()
    .filter(
      (s) =>
        (!query.get("carrierId") || s.carrierId === query.get("carrierId")) &&
        (query.get("active") !== "true" || s.active) &&
        (!query.get("code") || s.code === query.get("code")!.toUpperCase()) &&
        (q === "" || s.code.toLowerCase().includes(q) || s.name.toLowerCase().includes(q)),
    )
    .sort((a, b) => a.carrierId.localeCompare(b.carrierId) || a.code.localeCompare(b.code));
  return { items: rows.slice(0, Number(query.get("limit") ?? 100)) };
});
route("POST", `${BASE}/carrier-services`, ({ body }) => {
  const input = body as { carrierId: string; code: string; name: string; mode: CarrierService["mode"] };
  const code = (input.code ?? "").trim().toUpperCase();
  if (services.all().some((s) => s.carrierId === input.carrierId && s.code === code)) return reply.problem(409, "TMS-0140", "this carrier already has a service with that code");
  return reply.created(services.insert({ id: crypto.randomUUID(), carrierId: input.carrierId, code, name: input.name.trim(), mode: input.mode, active: true }));
});
route("GET", `${BASE}/carrier-services/{id}`, ({ params }) => services.get(params.id) ?? reply.problem(404, "TMS-0100", "carrier service not found"));

const loads = table<Load>([
  { id: uid(K_LOAD, 1), mode: "LTL", status: "ACCEPTED", shipFromSiteId: PLANT.id, shipToRegion: "US-SE", pickupDate: day(-6), totalWeightKg: "4200", totalVolumeM3: "18", pallets: 6, capacity: { maxWeightKg: "20000", maxVolumeM3: "60", maxPallets: 26 }, shipmentIds: [], version: 3, createdAt: at(-8) },
  { id: uid(K_LOAD, 2), mode: "TL", status: "DELIVERED", shipFromSiteId: PLANT.id, shipToRegion: "US-W", pickupDate: day(-14), totalWeightKg: "17800", totalVolumeM3: "52", pallets: 22, capacity: { maxWeightKg: "20000", maxVolumeM3: "60", maxPallets: 26 }, shipmentIds: [], version: 5, createdAt: at(-16) },
]);
route("GET", `${BASE}/loads`, () => ({ items: loads.all() }));
route("GET", `${BASE}/loads/{id}`, ({ params }) => loads.get(params.id) ?? reply.problem(404, "TMS-0100", "load not found"));

const bill = (n: number, load: number, carrierN: number, total: string, status: FreightBill["matchStatus"], version = 1): FreightBill => ({
  id: uid(K_BILL, n),
  billNumber: `FB-${String(40 + n).padStart(6, "0")}`,
  loadId: uid(K_LOAD, load),
  carrierId: carrier(carrierN),
  currency: "USD",
  expectedLines: [
    { chargeCode: "BASE", amount: (Number(total) * 0.86).toFixed(2) },
    { chargeCode: "FUEL", amount: (Number(total) * 0.14).toFixed(2) },
  ],
  expectedTotal: total,
  matchStatus: status,
  version,
  createdAt: at(-5 - n),
});
const bills = table<FreightBill>([bill(1, 1, 4, "684.50", "UNMATCHED"), bill(2, 2, 5, "2210.00", "MATCHED", 2), bill(3, 1, 4, "512.75", "VARIANCE", 2)]);

route("GET", `${BASE}/freight-bills`, ({ query }) => {
  const q = (query.get("q") ?? "").trim().toLowerCase();
  const rows = bills
    .all()
    .filter(
      (b) =>
        (!query.get("matchStatus") || b.matchStatus === query.get("matchStatus")) &&
        (!query.get("carrierId") || b.carrierId === query.get("carrierId")) &&
        (!query.get("loadId") || b.loadId === query.get("loadId")) &&
        (!query.get("number") || b.billNumber === query.get("number")) &&
        (q === "" || b.billNumber.toLowerCase().includes(q)),
    )
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.slice(0, Number(query.get("limit") ?? 500)) };
});
route("GET", `${BASE}/freight-bills/{id}`, ({ params }) => {
  const b = bills.get(params.id);
  return b ? reply.versioned({ ...b, decisions: [] }, b.version) : reply.problem(404, "TMS-0100", "freight bill not found");
});

const card = (n: number, carrierN: number, serviceN: number): RateCard => ({
  id: uid(K_CARD, n),
  carrierId: carrier(carrierN),
  serviceId: uid(K_SERVICE, serviceN),
  basis: "LANE",
  currency: "USD",
  validFrom: day(-90),
  validTo: null,
  minimumCharge: "85.00",
  dimDivisor: 5000,
  fuelPct: "14.00",
  rows: [{ origin: "CR", destination: "US-SE", breakFrom: "0", breakTo: null, rate: "0.42", unit: "KG" }],
  accessorials: [{ code: "LIFTGATE", flat: "45.00", pct: "0" }],
});
route("GET", `${BASE}/rate-cards`, () => ({ items: [card(1, 4, 1), card(2, 4, 2), card(3, 5, 3)] }));
