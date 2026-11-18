import { describe, expect, it } from "vitest";
import { Metrics } from "../src/metrics.ts";

describe("Metrics", () => {
  it("renders counters and gauges with their types; a gauge keeps the last value", () => {
    const m = new Metrics();
    m.set("kafka_consumer_running", "1 while the consumer runs", 0);
    m.inc("kafka_consumer_start_failures_total", "Failed starts", {}, 0);
    m.inc("kafka_consumer_start_failures_total", "Failed starts");
    m.set("kafka_consumer_running", "1 while the consumer runs", 1);
    expect(m.render()).toBe(
      [
        "# HELP kafka_consumer_running 1 while the consumer runs",
        "# TYPE kafka_consumer_running gauge",
        "kafka_consumer_running 1",
        "# HELP kafka_consumer_start_failures_total Failed starts",
        "# TYPE kafka_consumer_start_failures_total counter",
        "kafka_consumer_start_failures_total 1",
        "",
      ].join("\n"),
    );
  });
});
