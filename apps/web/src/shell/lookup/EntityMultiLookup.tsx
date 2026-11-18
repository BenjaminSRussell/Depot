import { useEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { Input } from "../ui";
import { plural } from "./EntityLookup";
import lookup from "./EntityLookup.module.css";
import styles from "./EntityMultiLookup.module.css";
import {
  findByNumber,
  formatOption,
  lookupDefs,
  rememberOption,
  resolveOption,
  type LookupEntityKey,
  type LookupOption,
  type LookupScope,
} from "./entities";

const DEBOUNCE_MS = 200;
const DEFAULT_CAP = 1000;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SEPARATORS = /[\s,;]+/;

export interface EntityMultiLookupProps {
  entity: LookupEntityKey;
  id?: string;
  value: string[];
  onChange(ids: string[], options: LookupOption[]): void;
  scope?: LookupScope;
  cap?: number;
  disabled?: boolean;
  invalid?: boolean;
  placeholder?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-required"?: boolean;
  "aria-invalid"?: boolean | "true" | "false";
}

export function EntityMultiLookup({
  entity,
  id = `multi-${entity}`,
  value,
  onChange,
  scope,
  cap = DEFAULT_CAP,
  disabled,
  invalid,
  placeholder,
  ...aria
}: EntityMultiLookupProps) {
  const def = lookupDefs[entity];
  const ids = Array.isArray(value) ? value : [];
  const listId = `${id}-listbox`;
  const [names, setNames] = useState<Record<string, LookupOption>>({});
  const [typed, setTyped] = useState("");
  const [results, setResults] = useState<LookupOption[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [failed, setFailed] = useState(false);
  const [note, setNote] = useState("");
  const latest = useRef({ ids, names });
  latest.current = { ids, names };

  useEffect(() => {
    const missing = ids.filter((chosen) => !names[chosen]);
    let live = true;
    for (const chosen of missing) {
      void resolveOption(entity, chosen).then((entry) => {
        if (live && entry) setNames((known) => ({ ...known, [chosen]: entry }));
      });
    }
    return () => {
      live = false;
    };
  }, [entity, ids.join("|")]);

  const query = typed.trim();
  const scopeKey = JSON.stringify(scope ?? {});
  useEffect(() => {
    if (!open || query === "") {
      setResults([]);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      def.search(query, scope ?? {}, controller.signal).then(
        (found) => {
          setFailed(false);
          setResults(found);
          setActive(0);
        },
        () => {
          if (!controller.signal.aborted) {
            setResults([]);
            setFailed(true);
          }
        },
      );
    }, DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [open, query, scopeKey, def]);

  const choices = results.filter((entry) => !ids.includes(entry.id));

  function add(entries: LookupOption[]) {
    const fresh = entries.filter((entry, at) => !latest.current.ids.includes(entry.id) && entries.findIndex((e) => e.id === entry.id) === at);
    if (fresh.length === 0) return;
    fresh.forEach((entry) => rememberOption(entity, entry));
    const known = { ...latest.current.names, ...Object.fromEntries(fresh.map((entry) => [entry.id, entry])) };
    setNames(known);
    const nextIds = [...latest.current.ids, ...fresh.map((entry) => entry.id)];
    onChange(nextIds, nextIds.flatMap((chosen) => (known[chosen] ? [known[chosen]] : [])));
  }

  function choose(entry: LookupOption) {
    add([entry]);
    setTyped("");
    setOpen(false);
    setNote("");
  }

  function remove(chosen: string) {
    const nextIds = ids.filter((x) => x !== chosen);
    onChange(nextIds, nextIds.flatMap((x) => (names[x] ? [names[x]] : [])));
  }

  async function paste(text: string) {
    const seen = new Map(text.split(SEPARATORS).filter((token) => token !== "").map((token) => [token.toLowerCase(), token]));
    const tokens = [...seen.values()];
    if (tokens.length === 0) return;
    if (tokens.length > cap - ids.length) {
      setNote(`Add at most ${cap} ${plural(def.noun)}.`);
      return;
    }
    let found: (LookupOption | null)[];
    try {
      found = await Promise.all(tokens.map((token) => (UUID.test(token) ? resolveOption(entity, token.toLowerCase()) : findByNumber(entity, token))));
    } catch {
      setNote(`Could not look up the pasted ${plural(def.noun)}. Try again.`);
      return;
    }
    const matched = found.filter((entry): entry is LookupOption => entry !== null);
    add(matched);
    const missing = tokens.filter((_, at) => found[at] === null);
    if (tokens.length === 1 && missing.length === 1) {
      setTyped(tokens[0]);
      setOpen(true);
      setNote("");
      return;
    }
    setNote(missing.length > 0 ? `Not found: ${missing.join(", ")}` : "");
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      setOpen(true);
      setActive((at) => Math.max(0, Math.min(at + (event.key === "ArrowDown" ? 1 : -1), choices.length - 1)));
    } else if (event.key === "Enter" && open) {
      event.preventDefault();
      if (choices[active]) choose(choices[active]);
    } else if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      setTyped("");
    }
  }

  function onPaste(event: ClipboardEvent<HTMLInputElement>) {
    const text = event.clipboardData.getData("text");
    if (text.trim() === "") return;
    event.preventDefault();
    void paste(text);
  }

  const searchStatus = failed
    ? `Could not load ${def.noun} matches. Keep typing to try again.`
    : query === ""
      ? ""
      : choices.length === 0
        ? `No ${def.noun} found.`
        : `${choices.length} ${choices.length === 1 ? "match" : "matches"}.`;

  return (
    <div className={lookup.root}>
      {ids.length > 0 ? (
        <ul className={styles.chips} aria-label={`Chosen ${plural(def.noun)}`}>
          {ids.map((chosen) => {
            const label = names[chosen] ? formatOption(names[chosen]) : `Selected ${def.noun}`;
            return (
              <li key={chosen} className={styles.chip}>
                <span>{label}</span>
                {disabled ? null : (
                  <button type="button" className={styles.remove} aria-label={`Remove ${label}`} onClick={() => remove(chosen)}>
                    ×
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      ) : null}
      <Input
        id={id}
        role="combobox"
        aria-expanded={open && query !== ""}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && choices[active] ? `${id}-option-${active}` : undefined}
        aria-label={aria["aria-label"]}
        aria-describedby={aria["aria-describedby"]}
        aria-required={aria["aria-required"]}
        aria-invalid={aria["aria-invalid"]}
        autoComplete="off"
        invalid={invalid}
        disabled={disabled}
        placeholder={placeholder ?? `Search ${plural(def.noun)} by name or number, or paste numbers`}
        value={typed}
        onFocus={() => setOpen(true)}
        onChange={(e) => {
          setTyped(e.target.value);
          setNote("");
          setOpen(true);
        }}
        onBlur={() => {
          window.setTimeout(() => setOpen(false), 120);
        }}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
      />
      {open && query !== "" ? (
        <ul id={listId} role="listbox" aria-label={`${def.noun} matches`} className={lookup.listbox}>
          {choices.map((entry, at) => (
            <li
              key={entry.id}
              id={`${id}-option-${at}`}
              role="option"
              aria-selected={at === active}
              className={lookup.option}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => choose(entry)}
              onMouseMove={() => setActive(at)}
            >
              <span>{entry.name}</span>
              {entry.number ? <span className={lookup.number}>{entry.number}</span> : null}
            </li>
          ))}
          <li role="presentation" className={lookup.status}>
            {searchStatus}
          </li>
        </ul>
      ) : null}
      <div role="status" aria-live="polite" className={styles.note}>
        {note || (open ? searchStatus : "")}
      </div>
    </div>
  );
}
