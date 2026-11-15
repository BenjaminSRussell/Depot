import { StatTile } from "../../shell/ui";
import { useWorkContext } from "../../shell/lookup/workContext";
import { fetchDso } from "./api/prediction";
import { useLoad } from "./useLoad";

/**
 * The forecast days to pay (DSO, #47) of the legal entity chosen in the header: the amount-weighted mean of the predicted payment
 * date less the invoice date over its open invoices, with the share the model expects to pay more than 30 days late. It appears
 * once the payment prediction service has scored the entity's open invoices; a failure leaves the tile saying so, not the page.
 */
export function DsoTile() {
  const { legalEntityId } = useWorkContext();
  const state = useLoad(async (signal) => (legalEntityId === "" ? null : fetchDso(legalEntityId, signal)), legalEntityId);
  if (legalEntityId === "") return <StatTile label="Forecast days to pay" value="—" hint="Choose a legal entity in the header." />;
  if (state.error) return <StatTile label="Forecast days to pay" value="—" tone="warning" hint="Could not load the forecast." />;
  if (state.loading && !state.data) return <StatTile label="Forecast days to pay" value="…" />;
  const f = state.data;
  if (!f) return <StatTile label="Forecast days to pay" value="—" hint="Not scored yet: the prediction service has not read this entity's open invoices." />;
  const late = Math.round(Number(f.avgPLate30d) * 100);
  return (
    <StatTile
      label="Forecast days to pay"
      value={f.dsoDays.toFixed(1)}
      tone={f.stale ? "warning" : "neutral"}
      hint={`${f.openInvoices} open invoices; ${late}% expected over 30 days late${f.stale ? "; the forecast is more than 7 days old" : ""}.`}
    />
  );
}
