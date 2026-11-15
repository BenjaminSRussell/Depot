import { forwardRef, useMemo, type SelectHTMLAttributes } from "react";
import { Select } from "./Select";

/** Zones offered when the runtime cannot list them (old browsers, test environments). */
const FALLBACK_ZONES = [
  "UTC",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Phoenix",
  "America/Anchorage",
  "America/Toronto",
  "America/Mexico_City",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Dublin",
  "Europe/Paris",
  "Europe/Berlin",
  "Europe/Madrid",
  "Europe/Warsaw",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
];

/** IANA zone names the runtime knows, UTC first. */
export function timeZoneNames(): string[] {
  let names: string[] = [];
  try {
    const supported = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf;
    if (supported) names = supported("timeZone");
  } catch {
    names = [];
  }
  if (names.length === 0) names = FALLBACK_ZONES;
  return ["UTC", ...names.filter((n) => n !== "UTC")];
}

/** True for a name the runtime accepts as an IANA time zone ("Local" and blanks are not zones). */
export function isTimeZone(name: string): boolean {
  if (name.trim() === "" || name === "Local" || name.length > 64) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: name });
    return true;
  } catch {
    return false;
  }
}

export interface TimeZoneSelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "value"> {
  value: string;
  invalid?: boolean;
}

/** Picks an IANA time zone by name. A saved zone the list does not contain stays selectable, so an edit never drops it. */
export const TimeZoneSelect = forwardRef<HTMLSelectElement, TimeZoneSelectProps>(function TimeZoneSelect({ value, ...rest }, ref) {
  const zones = useMemo(() => timeZoneNames(), []);
  const options = value !== "" && !zones.includes(value) ? [value, ...zones] : zones;
  return (
    <Select {...rest} ref={ref} value={value}>
      {options.map((zone) => (
        <option key={zone} value={zone}>
          {zone}
        </option>
      ))}
    </Select>
  );
});
