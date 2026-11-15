import { apiFetch } from "../../../shell/api/http";
import { ReceivablesApiError } from "./client";

/** The forecast days to pay of a legal entity's open invoices (prediction service, GET /api/v1/predictions/payment/dso). */
export interface DsoForecast {
  legalEntityId: string;
  dsoDays: number;
  openInvoices: number;
  openAmount: string;
  avgPLate30d: string;
  scoredAt: string;
  stale: boolean;
}

const DECIMAL = /^\d+(\.\d+)?$/;

export function parseDso(raw: unknown): DsoForecast {
  const o = raw as Partial<DsoForecast> | null;
  if (
    !o ||
    typeof o.legalEntityId !== "string" ||
    typeof o.dsoDays !== "number" ||
    !Number.isFinite(o.dsoDays) ||
    !Number.isInteger(o.openInvoices) ||
    typeof o.openAmount !== "string" ||
    !DECIMAL.test(o.openAmount) ||
    typeof o.avgPLate30d !== "string" ||
    !DECIMAL.test(o.avgPLate30d) ||
    typeof o.scoredAt !== "string" ||
    typeof o.stale !== "boolean"
  ) {
    throw new ReceivablesApiError(502, "COM-0001", "The days to pay forecast has an unexpected shape.");
  }
  return o as DsoForecast;
}

/** The forecast, or null while none of the legal entity's open invoices has been scored (404). */
export async function fetchDso(legalEntityId: string, signal?: AbortSignal): Promise<DsoForecast | null> {
  const res = await apiFetch(`/api/v1/predictions/payment/dso?legalEntityId=${encodeURIComponent(legalEntityId)}`, { signal });
  if (res.status === 404) return null;
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { code?: string; detail?: string };
    throw new ReceivablesApiError(res.status, body.code ?? "COM-0001", body.detail ?? "The days to pay forecast could not be loaded.");
  }
  return parseDso(await res.json());
}
