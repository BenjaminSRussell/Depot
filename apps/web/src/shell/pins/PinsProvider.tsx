import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { copy } from "../copy";
import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { RECORD_TYPE_NOUN } from "../search/records";
import { Button } from "../ui";
import { addPin, MAX_PINS, moveAmong, pinKey, probeStorage, readPins, removePin, type Pin, type PinChange, type PinTarget } from "./pinStore";
import { pinTargetFor } from "./pinTargets";

interface PinsValue {
  available: boolean;
  pins: readonly Pin[];
  target: PinTarget | null;
  pinned: boolean;
  toggle(): void;
  move(key: string, visible: readonly string[], direction: -1 | 1, message: string): void;
  remove(key: string, message: string): void;
  canOpen(to: string): boolean;
  pageLabel(route: string): string | undefined;
}

const unavailable: PinsValue = {
  available: false,
  pins: [],
  target: null,
  pinned: false,
  toggle: () => undefined,
  move: () => undefined,
  remove: () => undefined,
  canOpen: () => false,
  pageLabel: () => undefined,
};

const PinsContext = createContext<PinsValue>(unavailable);

export const usePins = (): PinsValue => useContext(PinsContext);

export interface PinsProviderProps {
  identity: string;
  canOpen(to: string): boolean;
  pageLabels: ReadonlyMap<string, { label: string }>;
  literalRoutes?: ReadonlySet<string>;
  announce(message: string, visual?: boolean): void;
  storage?: PreferenceStorage;
  children: ReactNode;
}

/** The pages and records this person pinned, kept in this browser only, per signed-in identity. */
export function PinsProvider({ identity, canOpen, pageLabels, literalRoutes, announce, storage = localPreferenceStorage, children }: PinsProviderProps) {
  const { pathname } = useLocation();
  const [available] = useState(() => identity !== "" && probeStorage(storage));
  const [pins, setPins] = useState<Pin[]>(() => (available ? readPins(identity, storage) : []));
  const pageLabel = useCallback((route: string) => pageLabels.get(route)?.label, [pageLabels]);
  const target = useMemo(() => (canOpen(pathname) ? pinTargetFor(pathname, pageLabels.has(pathname), literalRoutes) : null), [canOpen, pathname, pageLabels, literalRoutes]);
  const key = target === null ? null : pinKey(target);
  const pinned = key !== null && pins.some((p) => pinKey(p) === key);

  const apply = useCallback(
    (change: PinChange, message: string) => {
      if (change.ok) {
        setPins(change.pins);
        announce(message);
      } else {
        announce(change.reason === "limit" ? copy.pins.limit(MAX_PINS) : copy.pins.saveFailed, true);
      }
    },
    [announce],
  );

  const value = useMemo<PinsValue>(() => {
    if (!available) return unavailable;
    const label = target === null ? "" : target.kind === "page" ? (pageLabel(target.route) ?? "") : RECORD_TYPE_NOUN[target.recordType];
    return {
      available,
      pins,
      target,
      pinned,
      canOpen,
      pageLabel,
      toggle: () => {
        if (target === null || key === null) return;
        if (pinned) apply(removePin(identity, key, storage), copy.pins.unpinned(label));
        else apply(addPin(identity, target, storage), copy.pins.pinned(label));
      },
      move: (moving, visible, direction, message) => apply(moveAmong(identity, moving, visible, direction, storage), message),
      remove: (removing, message) => apply(removePin(identity, removing, storage), message),
    };
  }, [available, pins, target, key, pinned, canOpen, pageLabel, apply, identity, storage]);

  return <PinsContext.Provider value={value}>{children}</PinsContext.Provider>;
}

export function PinToggle() {
  const { available, target, pinned, toggle } = usePins();
  if (!available || target === null) return null;
  return (
    <Button variant="ghost" size="sm" aria-pressed={pinned} onClick={toggle}>
      {pinned ? copy.pins.unpin : copy.pins.pin}
    </Button>
  );
}
