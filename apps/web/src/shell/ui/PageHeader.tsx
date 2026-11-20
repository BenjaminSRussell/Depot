import { lazy, Suspense, type ReactNode } from "react";
import styles from "./PageHeader.module.css";

const LazyAskAboutRecord = lazy(() => import("../assist/AskAboutRecord").then((m) => ({ default: m.AskAboutRecord })));

export interface PageHeaderProps {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
  askAbout?: { entity: string; id: string } | undefined;
}

export function PageHeader({ title, description, actions, meta, askAbout }: PageHeaderProps) {
  return (
    <header className={styles.header}>
      <div className={styles.text}>
        <div className={styles.titleRow}>
          <h2 className={styles.title}>{title}</h2>
          {meta}
        </div>
        {description ? <div className={styles.description}>{description}</div> : null}
      </div>
      {actions || askAbout ? (
        <div className={styles.actions}>
          {actions}
          {askAbout ? (
            <Suspense fallback={null}>
              <LazyAskAboutRecord entity={askAbout.entity} id={askAbout.id} />
            </Suspense>
          ) : null}
        </div>
      ) : null}
    </header>
  );
}
