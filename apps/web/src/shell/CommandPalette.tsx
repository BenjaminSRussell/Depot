import { useEffect, useMemo, useRef, useState } from "react";
import styles from "./CommandPalette.module.css";
import { copy } from "./copy";
import { trackSearchNoResults } from "./uxEvents";
import { useFocusTrap } from "./hooks/useFocusTrap";
import { isFavourite, pushRecent, readFavourites, readRecent, toggleFavourite } from "./search/recentRecords";
import { buildSections } from "./search/paletteResults";
import { MIN_RECORD_QUERY, RECORD_TYPE_NOUN, searchRecords as defaultSearchRecords, type RecordHit, type RecordSearch } from "./search/records";
import type { AvailabilityState } from "./assistant/availability";
import { Button, Icon, type IconName } from "./ui";

export interface PaletteEntry {
  id: string;
  group: string;
  label: string;
  to: string;
  icon?: IconName;
  keywords?: string;
  /** Set on a record result: the document or master record the entry opens. */
  record?: RecordHit;
  /** Status and key fields shown after the label, for a record result. */
  detail?: string;
  /** Set on the assistant row: choosing it sends the typed text to the assistant. */
  ask?: boolean;
}

export interface CommandPaletteProps {
  entries: PaletteEntry[];
  onSelect(entry: PaletteEntry): void;
  onClose(): void;
  announce(message: string): void;
  /** Whose recent and favourite records are kept; empty keeps none. */
  identity?: string;
  /** Finds records by number or name. Defaults to the search service. */
  searchRecords?: RecordSearch;
  /** Sends the typed text to the assistant. Without it the bar has no assistant group. */
  onAsk?(text: string): void;
  assistantState?: AvailabilityState;
}

const SEARCH_DELAY_MS = 200;
const ANNOUNCE_DELAY_MS = 250;

function optionLabel(entry: PaletteEntry, favourites: RecordHit[]): string {
  if (!entry.record) {
    return `${entry.label}, ${entry.group}`;
  }
  const parts = [`${RECORD_TYPE_NOUN[entry.record.type]} ${entry.label}`, entry.detail ?? "", isFavourite(favourites, entry.record) ? copy.palette.favouriteState : ""];
  return parts.filter(Boolean).join(", ");
}

export function CommandPalette({ entries, onSelect, onClose, announce, identity = "", searchRecords = defaultSearchRecords, onAsk, assistantState }: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [hits, setHits] = useState<RecordHit[]>([]);
  const [searchFailed, setSearchFailed] = useState(false);
  const [favourites, setFavourites] = useState<RecordHit[]>(() => readFavourites(identity));
  const [recent] = useState<RecordHit[]>(() => readRecent(identity));
  const panelRef = useRef<HTMLDivElement>(null);
  const announcedQuery = useRef("");
  const needle = query.trim();
  const searching = needle.length >= MIN_RECORD_QUERY;
  const assistantOn = onAsk !== undefined && assistantState?.status === "enabled";
  const sections = useMemo(
    () => buildSections({ entries, hits: searching ? hits : [], recents: recent, favourites, query, assistant: assistantOn }),
    [entries, searching, hits, recent, favourites, query, assistantOn],
  );
  const groups = useMemo(() => {
    let next = 0;
    return sections.map((s) => ({ ...s, items: s.items.map((entry) => ({ entry, index: next++ })) }));
  }, [sections]);
  const visible = useMemo(() => sections.flatMap((s) => s.items), [sections]);
  const resultCount = visible.filter((e) => !e.ask).length;
  const hasResults = visible.length > 0;
  const assistantChecking = onAsk !== undefined && assistantState?.status === "checking" && needle !== "";
  const assistantNote =
    onAsk !== undefined && (assistantState?.status === "degraded" || assistantState?.status === "unavailable")
      ? copy.palette.assistantUnavailable[assistantState.reason]
      : undefined;

  useFocusTrap(panelRef, true);

  useEffect(() => {
    if (!searching) {
      setHits([]);
      setSearchFailed(false);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      searchRecords(needle, controller.signal).then(
        (found) => {
          if (!controller.signal.aborted) {
            setHits(found);
            setSearchFailed(false);
          }
        },
        () => {
          if (!controller.signal.aborted) {
            setHits([]);
            setSearchFailed(true);
          }
        },
      );
    }, SEARCH_DELAY_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [needle, searching, searchRecords]);

  useEffect(() => {
    if (query === announcedQuery.current) {
      return;
    }
    const timer = window.setTimeout(() => {
      announcedQuery.current = query;
      if (resultCount === 0) trackSearchNoResults(query);
      announce(resultCount === 0 ? copy.announce.noResults : resultCount === 1 ? copy.announce.oneResult : copy.announce.results(resultCount));
    }, ANNOUNCE_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [query, resultCount, announce]);

  useEffect(() => {
    document.getElementById(`palette-option-${active}`)?.scrollIntoView?.({ block: "nearest" });
  }, [active, visible]);

  const activeEntry = hasResults ? visible[Math.min(active, visible.length - 1)] : undefined;
  const activeRecord = activeEntry?.record;

  function choose(entry: PaletteEntry) {
    if (entry.ask) {
      onAsk?.(needle);
      onClose();
      return;
    }
    if (entry.record) {
      pushRecent(identity, entry.record);
    }
    onSelect(entry);
  }

  function toggleActiveFavourite() {
    if (!activeRecord || identity === "") {
      return;
    }
    const next = toggleFavourite(identity, activeRecord);
    setFavourites(next);
    announce(isFavourite(next, activeRecord) ? copy.announce.favouriteAdded(activeRecord.number) : copy.announce.favouriteRemoved(activeRecord.number));
  }

  function move(delta: number) {
    if (!hasResults) {
      return;
    }
    setActive((current) => (current + delta + visible.length) % visible.length);
  }

  return (
    <div
      className={styles.overlay}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose();
        }
      }}
    >
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="palette-title" className={styles.panel}>
        <h2 id="palette-title" className="visually-hidden">
          {copy.palette.title}
        </h2>
        <div className={styles.inputRow}>
          <span className={styles.searchIcon}>
            <Icon name="search" size={20} />
          </span>
          <label htmlFor="global-search" className="visually-hidden">
            {copy.palette.inputLabel}
          </label>
          <input
            id="global-search"
            className={styles.input}
            role="combobox"
            aria-expanded={hasResults}
            aria-controls="palette-listbox"
            aria-autocomplete="list"
            aria-activedescendant={hasResults ? `palette-option-${active}` : undefined}
            aria-describedby="palette-help"
            autoComplete="off"
            spellCheck={false}
            placeholder={copy.palette.placeholder}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                move(1);
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                move(-1);
              } else if (e.key === "Enter") {
                e.preventDefault();
                if (activeEntry) {
                  choose(activeEntry);
                }
              } else if ((e.key === "Home" || e.key === "End") && !e.ctrlKey) {
                e.preventDefault();
                setActive(e.key === "Home" ? 0 : Math.max(visible.length - 1, 0));
              } else if (e.altKey && e.key.toLowerCase() === "f") {
                e.preventDefault();
                toggleActiveFavourite();
              } else if (e.key === "Escape") {
                e.stopPropagation();
                onClose();
              }
            }}
          />
          <Button variant="ghost" size="sm" aria-label={copy.buttons.closeSearch} onClick={onClose}>
            <Icon name="close" />
          </Button>
        </div>
        <p id="palette-help" className="visually-hidden">
          {copy.palette.help}
        </p>
        <div
          id="palette-listbox"
          role="listbox"
          aria-label={copy.palette.listLabel}
          className={styles.list}
          onMouseDown={(e) => e.preventDefault()}
        >
          {groups.map((g, gi) => (
            <div key={g.id} role="group" aria-labelledby={`palette-group-${gi}`}>
              <div id={`palette-group-${gi}`} role="presentation" className={styles.groupLabel}>
                {g.label}
              </div>
              {g.items.map(({ entry, index }) => (
                <div
                  key={entry.id}
                  role="option"
                  id={`palette-option-${index}`}
                  aria-label={optionLabel(entry, favourites)}
                  aria-selected={index === active}
                  className={styles.option}
                  onMouseMove={() => setActive(index)}
                  onClick={() => choose(entry)}
                >
                  <span className={styles.optionIcon}>
                    <Icon name={entry.icon ?? "grid"} />
                  </span>
                  {entry.record ? (
                    <span className={styles.optionType}>{RECORD_TYPE_NOUN[entry.record.type]} </span>
                  ) : null}
                  <span className={styles.optionLabel}>{entry.label}</span>
                  {entry.detail ? " " : null}
                  {entry.detail ? <span className={styles.optionDetail}>{entry.detail}</span> : null}
                  {entry.record && isFavourite(favourites, entry.record) ? (
                    <span className={styles.favourite} aria-hidden="true">
                      ★
                    </span>
                  ) : null}
                  {index === active ? (
                    <span className={styles.enterHint} aria-hidden="true">
                      ↵
                    </span>
                  ) : null}
                </div>
              ))}
            </div>
          ))}
          {assistantChecking ? (
            <div role="group" aria-label={copy.palette.assistant}>
              <div role="option" aria-selected="false" aria-disabled="true" aria-label={copy.palette.assistantChecking} className={`${styles.option} ${styles.optionDisabled}`}>
                <span className={styles.optionLabel}>{copy.palette.assistantChecking}</span>
              </div>
            </div>
          ) : null}
        </div>
        {assistantNote ? <p className={styles.note}>{assistantNote}</p> : null}
        {searchFailed ? (
          <p className={styles.note} role="status">
            {copy.palette.recordsUnavailable}
          </p>
        ) : null}
        {activeRecord && identity !== "" ? (
          <div className={styles.actions}>
            <Button variant="ghost" size="sm" onMouseDown={(e) => e.preventDefault()} onClick={toggleActiveFavourite}>
              {isFavourite(favourites, activeRecord) ? copy.palette.unfavourite(activeRecord.number) : copy.palette.favourite(activeRecord.number)}
            </Button>
          </div>
        ) : null}
        {resultCount > 0 ? null : (
          <div className={styles.empty}>
            <Icon name="search" size={20} />
            <p className={styles.emptyTitle}>{copy.palette.noResults(query)}</p>
            <p className={styles.emptyHint}>{copy.palette.noResultsHint}</p>
          </div>
        )}
        <footer className={styles.footer} aria-hidden="true">
          <span>
            <kbd>↑</kbd> <kbd>↓</kbd> to move
          </span>
          <span>
            <kbd>↵</kbd> to open
          </span>
          {activeRecord && identity !== "" ? (
            <span>
              <kbd>Alt</kbd> <kbd>F</kbd> to favourite
            </span>
          ) : null}
          <span>
            <kbd>Esc</kbd> to close
          </span>
        </footer>
      </div>
    </div>
  );
}
