import { z } from "zod";
import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";

const id = z.string().regex(/^[A-Za-z0-9_.:-]{1,128}$/);
const route = z.string().regex(/^\/[A-Za-z0-9_\-./:]{0,199}$/);
const at = z.iso.datetime();
const count = z.number().int().min(0);

export const ASSISTANT_SCHEMAS = {
  pins: z.array(z.strictObject({ id, at })),
  activity: z.array(z.strictObject({ id, route, at, count })),
  briefingDay: z.array(z.strictObject({ day: z.string().regex(/^\d{4}-\d{2}-\d{2}$/), at })),
};

export type AssistantKind = keyof typeof ASSISTANT_SCHEMAS;
export type AssistantRows<K extends AssistantKind> = z.infer<(typeof ASSISTANT_SCHEMAS)[K]>;
export type AssistantWrite = { ok: true } | { ok: false; reason: "schema" | "no-identity" };

const keyFor = (kind: AssistantKind, identity: string) => `depot.assistant.${kind}.v1.${encodeURIComponent(identity)}`;

export function createAssistantStore(storage: PreferenceStorage = localPreferenceStorage) {
  const memory = new Map<string, string>();

  function stored(key: string): string | null {
    try {
      return storage.read(key) ?? memory.get(key) ?? null;
    } catch {
      return memory.get(key) ?? null;
    }
  }

  return {
    read<K extends AssistantKind>(kind: K, identity: string): AssistantRows<K> {
      const empty = [] as AssistantRows<K>;
      const raw = identity === "" ? null : stored(keyFor(kind, identity));
      if (raw === null) return empty;
      try {
        const parsed = ASSISTANT_SCHEMAS[kind].safeParse(JSON.parse(raw));
        return parsed.success ? (parsed.data as AssistantRows<K>) : empty;
      } catch {
        return empty;
      }
    },
    write<K extends AssistantKind>(kind: K, identity: string, rows: unknown): AssistantWrite {
      if (identity === "") return { ok: false, reason: "no-identity" };
      const parsed = ASSISTANT_SCHEMAS[kind].safeParse(rows);
      if (!parsed.success) return { ok: false, reason: "schema" };
      const key = keyFor(kind, identity);
      const raw = JSON.stringify(parsed.data);
      memory.set(key, raw);
      try {
        storage.write(key, raw);
      } catch {
        return { ok: true };
      }
      return { ok: true };
    },
  };
}
