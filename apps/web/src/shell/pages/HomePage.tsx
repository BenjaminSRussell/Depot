import { lazy, Suspense, useMemo, useRef } from "react";
import { Link, useNavigate, useOutletContext } from "react-router-dom";
import { postRoute } from "../../modules/copilot/api/client";
import { AssistantNotice, useAiAvailability } from "../assistant/AvailabilityProvider";
import { createPromptRouter } from "../assistant/router";
import { handOffPrompt } from "../assistant/session";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { AssistantPrompt } from "../home/AssistantPrompt";
import { ASSISTANT_DUTY, COPILOT_PATH } from "../home/assistantRouter";
import { plannedRouteFor } from "../plannedRoutes";
import homeStyles from "../home/home.module.css";
import { usePrincipalDuties } from "../principal";
import { useRecentPages } from "../recent";
import { isPathEnabled } from "../tenant/featureRules";
import { useTenantSettings } from "../tenant/TenantSettings";
import { Alert, Badge, Button, Card, CardGrid, EmptyState, Icon, PageHeader } from "../ui";
import { MyWorkSummary } from "./MyWorkSummary";
import { WorkspaceKpis } from "./WorkspaceKpis";
import { ALL_FEATURES_PATH, HOME_WORKSPACE_LIMIT, TOP_TASK_COUNT } from "../workspaces/model";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import styles from "./pages.module.css";

const SETUP_PATH = "/setup";

// Loaded only in the demo build, like the banner in main.tsx: a static import would put the demo company and its tour
// in every production bundle even though nothing renders them there (UXS-01).
const DemoTour = import.meta.env.MODE === "demo" ? lazy(() => import("../../demo/DemoTour").then((m) => ({ default: m.DemoTour }))) : null;

function SetupBanner() {
  const { setup } = useTenantSettings();
  if (setup === undefined || setup.complete) return null;
  return (
    <Alert
      tone="info"
      title={copy.home.setupTitle}
      action={
        <Link to={SETUP_PATH} className={styles.link}>
          {copy.home.setupLink}
        </Link>
      }
    >
      {copy.home.setupBody(setup.finished, setup.total)}
    </Alert>
  );
}

function Recent() {
  const { pages } = useRecentPages();
  return (
    <Card title={copy.home.recentTitle} headingLevel={2}>
      {pages.length === 0 ? (
        <EmptyState compact title={copy.home.recentTitle} description={copy.home.recentEmpty} />
      ) : (
        <ul className={styles.links} aria-label={copy.home.recentTitle}>
          {pages.map((p) => (
            <li key={p.to}>
              <Link to={p.to} className={styles.link}>
                <span>{p.label}</span>
                <Icon name="chevronRight" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/**
 * The home page is the role home. For the person's current workspace it shows what is waiting for them, a few
 * numbers that matter to the role, shortcuts to the top tasks and the pages they used last. Their other workspaces are one
 * click away but add no links, so the page stays short however many workspaces the person holds.
 */
export function HomePage() {
  const { held, active, setActive } = useWorkspaceNav();
  const navigate = useNavigate();
  const announce = useOutletContext<((message: string) => void) | undefined>();
  const { features } = useTenantSettings();
  const duties = usePrincipalDuties();
  const availability = useAiAvailability().state;
  const availabilityRef = useRef(availability);
  availabilityRef.current = availability;
  // TODO DEPOT-281, DEPOT-282, DEPOT-297: register the navigate and act handlers
  const router = useMemo(
    () =>
      createPromptRouter({
        handlers: {
          ask: (text) => {
            announce?.(copy.home.assistantLoading);
            handOffPrompt(text);
            navigate(COPILOT_PATH);
          },
        },
        getAvailability: () => availabilityRef.current,
        hasDuty: (duty) => holdsDuty(duties, duty),
        postRoute,
        searchFallback: (text) => {
          announce?.(copy.home.assistantSearchNote);
          navigate({ pathname: ALL_FEATURES_PATH, search: `?${new URLSearchParams({ q: text })}` });
        },
      }),
    [announce, duties, navigate],
  );
  const assistant =
    availability.status === "enabled" && holdsDuty(duties, ASSISTANT_DUTY) && isPathEnabled(features, COPILOT_PATH) && plannedRouteFor(COPILOT_PATH) === undefined;
  const ordered = (active ? [active, ...held.filter((w) => w.id !== active.id)] : held).slice(0, HOME_WORKSPACE_LIMIT);
  const cards =
    ordered.length === 0 ? (
      <Card>
        <EmptyState icon="grid" title={copy.home.emptyTitle} description={copy.home.emptyDescription} />
      </Card>
    ) : (
      <CardGrid as="ul" label={assistant ? copy.home.browseTitle : copy.home.pageTitle}>
        {ordered.map((w) => (
          <li key={w.id} className={styles.cell}>
            <Card>
              <div className={styles.cardHead}>
                <h3 className={styles.cardTitle}>{w.label}</h3>
                {w.id === active?.id ? <Badge tone="neutral">{copy.home.current}</Badge> : null}
              </div>
              <p className={styles.cardDescription}>{w.description}</p>
              {w.id === active?.id ? (
                <ul className={styles.links} aria-label={`${w.label}: ${copy.home.topTasks}`}>
                  {w.entries.slice(0, TOP_TASK_COUNT).map((n) => (
                    <li key={n.to}>
                      <Link to={n.to} className={styles.link}>
                        <span>{n.label}</span>
                        <Icon name="chevronRight" />
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <Button size="sm" onClick={() => setActive(w.id)} aria-label={copy.home.switchTo(w.label)}>
                  {copy.home.switchTo(w.label)}
                </Button>
              )}
            </Card>
          </li>
        ))}
      </CardGrid>
    );
  const seeAll = (
    <p>
      <Link to={ALL_FEATURES_PATH}>{copy.home.seeAll}</Link>
    </p>
  );
  const demoTour = DemoTour ? (
    <Suspense fallback={null}>
      <DemoTour />
    </Suspense>
  ) : null;
  if (!assistant) {
    return (
      <>
        <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
        <AssistantNotice />
        {demoTour}
        <SetupBanner />
        <MyWorkSummary />
        <WorkspaceKpis workspace={active} />
        {cards}
        <Recent />
        {seeAll}
      </>
    );
  }
  return (
    <>
      <PageHeader title={copy.home.pageTitle} description={copy.home.description} />
      <AssistantNotice />
      <AssistantPrompt onSubmit={router.submit} />
      {demoTour}
      <SetupBanner />
      <MyWorkSummary />
      <WorkspaceKpis workspace={active} />
      <section aria-labelledby="home-browse-title" className={homeStyles.browse}>
        <h2 id="home-browse-title" className={homeStyles.browseTitle}>
          {copy.home.browseTitle}
        </h2>
        {cards}
        {seeAll}
      </section>
      <Recent />
    </>
  );
}
