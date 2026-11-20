import type { PaletteEntry } from "../CommandPalette";
import { copy } from "../copy";
import type { IconName } from "../ui";
import { isFavourite } from "./recentRecords";
import type { RecordHit, RecordType } from "./records";

export const NAVIGATE_CAP = 5;

export type PaletteSectionId = "favourites" | "recent" | "navigate" | "records" | "assistant";

export interface PaletteSection {
  id: PaletteSectionId;
  label: string;
  items: PaletteEntry[];
}

export interface BuildSectionsInput {
  entries: PaletteEntry[];
  hits: RecordHit[];
  recents: RecordHit[];
  favourites: RecordHit[];
  query: string;
  assistant: boolean;
}

const RECORD_ICON: Record<RecordType, IconName> = {
  party: "database",
  product: "box",
  purchase_order: "note",
  receipt: "receipt",
  vendor_invoice: "receipt",
  sales_order: "note",
  packing_slip: "box",
  customer_invoice: "receipt",
  production_order: "cog",
  worker: "database",
};

const statusWord = (status: string) => status.charAt(0).toUpperCase() + status.slice(1).toLowerCase().replace(/_/g, " ");

export function recordEntry(hit: RecordHit, group: string): PaletteEntry {
  const label = hit.title && hit.title !== hit.number ? `${hit.number} ${hit.title}` : hit.number;
  const detail = [hit.status ? statusWord(hit.status) : "", hit.subtitle ?? ""].filter(Boolean).join(" · ");
  return { id: `record:${hit.type}:${hit.id}`, group, label, to: hit.link, icon: RECORD_ICON[hit.type], record: hit, detail: detail || undefined };
}

function rankOf(entry: PaletteEntry, needle: string): number | undefined {
  const label = entry.label.toLowerCase();
  if (label === needle) return 0;
  if (label.startsWith(needle)) return 1;
  if (label.split(/\s+/).some((word) => word.startsWith(needle))) return 2;
  if (label.includes(needle)) return 3;
  if ([entry.group, entry.keywords ?? ""].some((text) => text.toLowerCase().includes(needle))) return 4;
  return undefined;
}

export function rankNavigate(entries: PaletteEntry[], query: string): PaletteEntry[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return entries;
  return entries
    .map((entry) => ({ entry, rank: rankOf(entry, needle) }))
    .filter((r): r is { entry: PaletteEntry; rank: number } => r.rank !== undefined)
    .sort((a, b) => a.rank - b.rank)
    .map((r) => r.entry);
}

export function buildSections({ entries, hits, recents, favourites, query, assistant }: BuildSectionsInput): PaletteSection[] {
  const needle = query.trim();
  const navigate = rankNavigate(entries, needle);
  const sections: PaletteSection[] =
    needle === ""
      ? [
          { id: "favourites", label: copy.palette.favourites, items: favourites.map((h) => recordEntry(h, copy.palette.favourites)) },
          { id: "recent", label: copy.palette.recent, items: recents.filter((r) => !isFavourite(favourites, r)).map((h) => recordEntry(h, copy.palette.recent)) },
          { id: "navigate", label: copy.palette.navigate, items: navigate },
        ]
      : [
          { id: "navigate", label: copy.palette.navigate, items: navigate.slice(0, NAVIGATE_CAP) },
          { id: "records", label: copy.palette.records, items: hits.map((h) => recordEntry(h, copy.palette.records)) },
          {
            id: "assistant",
            label: copy.palette.assistant,
            items: assistant ? [{ id: "assistant:ask", group: copy.palette.assistant, label: copy.palette.askAssistant(needle), to: "", icon: "workflow", ask: true }] : [],
          },
        ];
  return sections.filter((s) => s.items.length > 0);
}
