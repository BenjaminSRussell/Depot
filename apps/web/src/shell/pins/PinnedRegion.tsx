import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { copy } from "../copy";
import { formatOption, resolveOption } from "../lookup/entities";
import { RECORD_TYPE_LABEL } from "../search/records";
import { Button, Card, EmptyState } from "../ui";
import styles from "./PinnedRegion.module.css";
import { usePins } from "./PinsProvider";
import { pinKey, type Pin } from "./pinStore";
import { pinHref, RECORD_LOOKUP } from "./pinTargets";

interface Shown {
  pin: Pin;
  key: string;
  label: string;
}

export function useLookedUpLabels(pins: readonly Pin[], canOpen: (to: string) => boolean): ReadonlyMap<string, string | null> {
  const [labels, setLabels] = useState<ReadonlyMap<string, string | null>>(new Map());
  useEffect(() => {
    let live = true;
    for (const pin of pins) {
      const entity = pin.kind === "record" ? RECORD_LOOKUP[pin.recordType] : undefined;
      if (pin.kind !== "record" || entity === undefined || !canOpen(pinHref(pin))) continue;
      void resolveOption(entity, pin.id).then((found) => {
        if (live) setLabels((current) => new Map(current).set(pinKey(pin), found === null ? null : formatOption(found)));
      });
    }
    return () => {
      live = false;
    };
  }, [pins, canOpen]);
  return labels;
}

export function PinnedRegion() {
  const { available, pins, canOpen, pageLabel, move, remove } = usePins();
  const looked = useLookedUpLabels(pins, canOpen);
  const regionRef = useRef<HTMLElement>(null);
  if (!available) return null;

  const shown = pins.flatMap<Shown>((pin) => {
    if (!canOpen(pinHref(pin))) return [];
    const key = pinKey(pin);
    const label = pin.kind === "page" ? pageLabel(pin.route) : RECORD_LOOKUP[pin.recordType] === undefined ? RECORD_TYPE_LABEL[pin.recordType] : looked.get(key);
    return typeof label === "string" ? [{ pin, key, label }] : [];
  });
  const keys = shown.map((s) => s.key);

  function shift({ key, label }: Shown, direction: -1 | 1) {
    const from = keys.indexOf(key);
    const to = from + direction;
    if (to < 0 || to >= keys.length) return;
    move(key, keys, direction, copy.pins.moved(label, direction === -1 ? "up" : "down", to + 1, keys.length));
  }

  return (
    <section ref={regionRef} tabIndex={-1} aria-label={copy.pins.title} className={styles.region}>
      <Card title={copy.pins.title} headingLevel={2}>
        {shown.length === 0 ? (
          <EmptyState compact title={copy.pins.title} description={copy.pins.empty} />
        ) : (
          <ol className={styles.list} aria-label={copy.pins.title}>
            {shown.map((item, index) => (
              <li key={item.key} className={styles.row}>
                <Link to={pinHref(item.pin)} className={styles.link}>
                  {item.label}
                </Link>
                <Button size="sm" variant="ghost" aria-disabled={index === 0 || undefined} aria-label={copy.pins.moveUpLabel(item.label)} onClick={() => shift(item, -1)}>
                  {copy.pins.moveUp}
                </Button>
                <Button size="sm" variant="ghost" aria-disabled={index === shown.length - 1 || undefined} aria-label={copy.pins.moveDownLabel(item.label)} onClick={() => shift(item, 1)}>
                  {copy.pins.moveDown}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={copy.pins.removeLabel(item.label)}
                  onClick={() => {
                    remove(item.key, copy.pins.removed(item.label));
                    regionRef.current?.focus();
                  }}
                >
                  {copy.pins.remove}
                </Button>
              </li>
            ))}
          </ol>
        )}
      </Card>
    </section>
  );
}
