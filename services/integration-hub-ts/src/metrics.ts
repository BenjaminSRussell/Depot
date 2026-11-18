// A tiny Prometheus text registry: counters and gauges with optional labels. No dependency.

type Family = { help: string; type: "counter" | "gauge"; values: Map<string, number> };

const labelKey = (labels: Record<string, string>): string =>
  Object.entries(labels)
    .map(([k, v]) => `${k}="${v.replace(/[\\"\n]/g, "_")}"`)
    .join(",");

export class Metrics {
  #families = new Map<string, Family>();

  #family(name: string, help: string, type: Family["type"]): Family {
    const family = this.#families.get(name) ?? { help, type, values: new Map<string, number>() };
    this.#families.set(name, family);
    return family;
  }

  inc(name: string, help: string, labels: Record<string, string> = {}, by = 1): void {
    const counter = this.#family(name, help, "counter");
    const key = labelKey(labels);
    counter.values.set(key, (counter.values.get(key) ?? 0) + by);
  }

  /** Sets a gauge. */
  set(name: string, help: string, value: number, labels: Record<string, string> = {}): void {
    this.#family(name, help, "gauge").values.set(labelKey(labels), value);
  }

  render(): string {
    const lines: string[] = [];
    for (const [name, { help, type, values }] of this.#families) {
      lines.push(`# HELP ${name} ${help}`, `# TYPE ${name} ${type}`);
      for (const [labels, value] of values) {
        lines.push(`${name}${labels === "" ? "" : `{${labels}}`} ${value}`);
      }
    }
    return `${lines.join("\n")}\n`;
  }
}
