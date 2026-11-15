// Payment prediction service (/api/v1/predictions/payment) for the demo: the forecast days to pay of the legal entity's open
// invoices. The model is not run here; the forecast is the amount-weighted mean of the days from invoice date to due date of the
// open receivables items plus the days late the demo customers are known to pay (Bluewater and Prairie are chronic late payers).
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { day, dec } from "../../store";
import { r2 } from "./arLedger";
import { daysBetween, openItems } from "./arModel";

const BASE = "/api/v1/predictions/payment";

route("GET", `${BASE}/dso`, ({ query }) => {
  const entity = query.get("legalEntityId") ?? "";
  if (entity !== LEGAL_ENTITY.id) return reply.problem(404, "AGT-0404", "no open invoices of this legal entity have been scored yet");
  const invoices = openItems().filter((i) => i.sourceType === "INVOICE" && Number(i.openAmount) > 0);
  if (invoices.length === 0) return reply.problem(404, "AGT-0404", "no open invoices of this legal entity have been scored yet");
  let total = 0;
  let days = 0;
  let late = 0;
  for (const i of invoices) {
    const amount = Number(i.openAmount);
    const overdue = Math.max(0, daysBetween(i.dueDate, day(0)));
    const expectedLate = overdue > 0 ? Math.min(40, overdue + 6) : 4;
    total += amount;
    days += amount * (daysBetween(i.documentDate, i.dueDate) + expectedLate);
    late += amount * Math.min(0.9, expectedLate / 60);
  }
  return reply.ok({
    legalEntityId: LEGAL_ENTITY.id,
    dsoDays: r2(days / total),
    openInvoices: invoices.length,
    openAmount: dec(total),
    avgPLate30d: (late / total).toFixed(4),
    scoredAt: new Date().toISOString(),
    stale: false,
  });
});
