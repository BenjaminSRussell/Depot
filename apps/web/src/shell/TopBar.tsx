import { useEffect, useState, type RefObject } from "react";
import { Link } from "react-router-dom";
import { UserMenu } from "./auth/account";
import { ContextSheet } from "./ContextSheet";
import { copy } from "./copy";
import type { PageMeta } from "./pageMeta";
import { HelpButton } from "./help/HelpPanel";
import { ContextPicker } from "./lookup/workContext";
import styles from "./Shell.module.css";
import { Button, Icon } from "./ui";

export interface TopBarProps {
  page: PageMeta;
  titleRef: RefObject<HTMLHeadingElement | null>;
  menuRef: RefObject<HTMLButtonElement | null>;
  drawerOpen: boolean;
  compact: boolean;
  showContext?: boolean;
  onOpenDrawer(): void;
  onToggleCompact(): void;
}

const PHONE_QUERY = "(max-width: 639px)";

function usePhone(): boolean {
  const supported = typeof window !== "undefined" && typeof window.matchMedia === "function";
  const [phone, setPhone] = useState(() => supported && window.matchMedia(PHONE_QUERY).matches);
  useEffect(() => {
    if (!supported) return;
    const query = window.matchMedia(PHONE_QUERY);
    const update = (event: MediaQueryListEvent) => setPhone(event.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, [supported]);
  return phone;
}

export function TopBar({ page, titleRef, menuRef, drawerOpen, compact, showContext = false, onOpenDrawer, onToggleCompact }: TopBarProps) {
  const phone = usePhone();
  const workspace = page.workspace && page.workspace.title !== page.title ? page.workspace : undefined;
  return (
    <div className={styles.topbar}>
      <button
        ref={menuRef}
        type="button"
        className={styles.menuButton}
        aria-label={copy.buttons.openNavigation}
        aria-expanded={drawerOpen}
        aria-controls="sidebar"
        onClick={onOpenDrawer}
      >
        <Icon name="menu" size={20} />
      </button>
      {workspace ? (
        <>
          <nav aria-label="Breadcrumb" className={styles.breadcrumb}>
            <ol>
              <li>
                <Link to={workspace.to}>{workspace.title}</Link>
              </li>
            </ol>
          </nav>
          <span className={styles.crumbSeparator} aria-hidden="true">
            <Icon name="chevronRight" />
          </span>
        </>
      ) : null}
      <h1 ref={titleRef} tabIndex={-1} className={styles.title}>
        {page.title}
      </h1>
      {phone ? (
        <div className={styles.topbarActions}>
          <ContextSheet showContext={showContext} compact={compact} onToggleCompact={onToggleCompact} />
        </div>
      ) : (
        <div className={styles.topbarActions}>
          {showContext ? <ContextPicker /> : null}
          <HelpButton />
          <Button variant="ghost" size="sm" aria-pressed={compact} className={styles.compactToggle} onClick={onToggleCompact}>
            {copy.buttons.compactView}
          </Button>
          <UserMenu />
        </div>
      )}
    </div>
  );
}
