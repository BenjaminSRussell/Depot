import { useEffect, useRef, useState } from "react";
import { UserMenu } from "./auth/account";
import { copy } from "./copy";
import { useFocusTrap } from "./hooks/useFocusTrap";
import { HelpButton } from "./help/HelpPanel";
import { ContextPicker } from "./lookup/workContext";
import styles from "./Shell.module.css";
import { Button, Icon } from "./ui";

export interface ContextSheetProps {
  showContext: boolean;
  compact: boolean;
  onToggleCompact(): void;
}

export function ContextSheet({ showContext, compact, onToggleCompact }: ContextSheetProps) {
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const sheet = useRef<HTMLDivElement>(null);
  const wasOpen = useRef(false);
  useFocusTrap(sheet, open);
  useEffect(() => {
    if (wasOpen.current && !open) opener.current?.focus();
    wasOpen.current = open;
  }, [open]);

  return (
    <>
      <button ref={opener} type="button" className={styles.contextButton} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)}>
        {copy.contextSheet.open}
      </button>
      {open ? (
        <>
          <div className={styles.sheetScrim} onClick={() => setOpen(false)} aria-hidden="true" />
          <div
            ref={sheet}
            role="dialog"
            aria-modal="true"
            aria-label={copy.contextSheet.title}
            className={styles.sheet}
            onKeyDown={(event) => {
              if (event.key === "Escape") setOpen(false);
            }}
          >
            <div className={styles.sheetHeader}>
              <h2 className={styles.sheetTitle}>{copy.contextSheet.title}</h2>
              <button type="button" className={styles.sheetClose} aria-label={copy.contextSheet.close} onClick={() => setOpen(false)}>
                <Icon name="close" size={20} />
              </button>
            </div>
            {showContext ? <ContextPicker /> : null}
            <div className={styles.sheetActions}>
              <HelpButton />
              <Button variant="ghost" size="sm" aria-pressed={compact} onClick={onToggleCompact}>
                {copy.buttons.compactView}
              </Button>
              <UserMenu />
            </div>
          </div>
        </>
      ) : null}
    </>
  );
}
