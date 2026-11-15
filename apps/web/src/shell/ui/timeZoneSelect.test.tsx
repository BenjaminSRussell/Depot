import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { isTimeZone, TimeZoneSelect, timeZoneNames } from "./TimeZoneSelect";

describe("time zone picker", () => {
  it("accepts IANA names and refuses blanks, Local and unknown names", () => {
    expect(isTimeZone("America/Chicago")).toBe(true);
    expect(isTimeZone("UTC")).toBe(true);
    expect(isTimeZone("")).toBe(false);
    expect(isTimeZone("Local")).toBe(false);
    expect(isTimeZone("Mars/Olympus")).toBe(false);
  });

  it("lists UTC first and keeps a saved zone the list does not hold", () => {
    expect(timeZoneNames()[0]).toBe("UTC");
    render(<TimeZoneSelect aria-label="Zone" value="Etc/Custom-9" onChange={() => undefined} />);
    expect((screen.getByLabelText("Zone") as HTMLSelectElement).value).toBe("Etc/Custom-9");
  });
});
