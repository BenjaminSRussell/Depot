const BOM = "\uFEFF";
const FORMULA_START = /^([\t\r]|[\s'"]*[=+\-@])/;
const PLAIN_NUMBER = /^[+-]?\d+(\.\d+)?$/;
const NEEDS_QUOTES = /[",\r\n]/;

export function csvCell(value: string | number | null | undefined): string {
  const text = value === null || value === undefined ? "" : String(value);
  const safe = FORMULA_START.test(text) && !PLAIN_NUMBER.test(text) ? `'${text}` : text;
  return NEEDS_QUOTES.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
}

export interface CsvMarking {
  notice: [string, string];
  column: string;
  label: string;
  cui: boolean[];
}

export function toCsv(header: string[], records: (string | number | null | undefined)[][], marking?: CsvMarking): string {
  const marked = marking ? records.map((record, i) => [...record, marking.cui[i] ? marking.label : ""]) : records;
  const head = marking ? [...header, marking.column] : header;
  const notice = marking && marking.cui.some(Boolean) ? [marking.notice] : [];
  const lines = [...notice, head, ...marked].map((record) => record.map(csvCell).join(","));
  return `${BOM}${lines.join("\r\n")}\r\n`;
}

export function downloadCsv(filename: string, content: string): void {
  const url = URL.createObjectURL(new Blob([content], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
