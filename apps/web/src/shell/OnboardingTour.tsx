import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { useLocation } from "react-router-dom";
import { useAiAvailability } from "./assistant/AvailabilityProvider";
import { copy } from "./copy";
import { holdsDuty } from "./dutyGate";
import { useFocusTrap } from "./hooks/useFocusTrap";
import { ASSISTANT_DUTY } from "./home/assistantRouter";
import { useSharedInbox } from "./inbox/InboxProvider";
import { selectSteps, shouldAutoStart, writeOnboarding, type StepId } from "./onboarding";
import styles from "./OnboardingTour.module.css";
import type { PreferenceStorage } from "./preferenceStorage";
import { usePrincipalDuties } from "./principal";
import { Button } from "./ui";

type OpenTour = (returnTo?: HTMLElement | null) => void;

const TourContext = createContext<OpenTour | null>(null);

export function useOpenTour(): OpenTour | null {
  return useContext(TourContext);
}

interface TourProviderProps {
  identity: string;
  storage: PreferenceStorage;
  titleRef: RefObject<HTMLElement | null>;
  children: ReactNode;
}

export function TourProvider({ identity, storage, titleRef, children }: TourProviderProps) {
  const { pathname } = useLocation();
  const duties = usePrincipalDuties();
  const aiStatus = useAiAvailability().state.status;
  const showsFeed = useSharedInbox() !== null;
  const [steps, setSteps] = useState<StepId[] | null>(null);
  const returnTo = useRef<HTMLElement | null>(null);
  const autoStartSettled = useRef(false);

  const open = useCallback<OpenTour>(
    (target) => {
      const focused = document.activeElement;
      returnTo.current = target ?? (focused instanceof HTMLElement && focused !== document.body ? focused : null);
      setSteps(selectSteps({ copilotDuty: holdsDuty(duties, ASSISTANT_DUTY), aiEnabled: aiStatus === "enabled", showsFeed }));
    },
    [duties, aiStatus, showsFeed],
  );

  useEffect(() => {
    if (autoStartSettled.current || aiStatus === "checking") return;
    if (shouldAutoStart({ identity, storage, pathname, aiStatus, demo: import.meta.env.MODE === "demo" })) {
      autoStartSettled.current = true;
      open();
    }
  }, [identity, storage, pathname, aiStatus, open]);

  function close(value: "skipped" | "completed") {
    autoStartSettled.current = true;
    writeOnboarding(identity, value, storage);
    setSteps(null);
    const opener = returnTo.current;
    const prompt = document.getElementById("home-assistant");
    const target = value === "completed" ? prompt ?? titleRef.current : opener?.isConnected ? opener : prompt ?? titleRef.current;
    target?.focus();
  }

  return (
    <TourContext.Provider value={open}>
      {children}
      {steps === null ? null : <TourDialog steps={steps} onClose={close} />}
    </TourContext.Provider>
  );
}

function TourDialog({ steps, onClose }: { steps: StepId[]; onClose(value: "skipped" | "completed"): void }) {
  const [index, setIndex] = useState(0);
  const panel = useRef<HTMLDivElement>(null);
  const nextButton = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const bodyId = useId();
  const total = steps.length;
  const last = index === total - 1;
  const step = copy.onboarding.steps[steps[index]];
  useFocusTrap(panel, true);

  useEffect(() => {
    if (!panel.current?.contains(document.activeElement)) nextButton.current?.focus();
  }, [index]);

  const back = () => setIndex((i) => Math.max(0, i - 1));
  const next = () => (last ? onClose("completed") : setIndex((i) => i + 1));

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onClose("skipped");
    } else if (event.key === "ArrowRight" && !last) {
      next();
    } else if (event.key === "ArrowLeft") {
      back();
    }
  };

  return (
    <div className={styles.overlay}>
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={bodyId} className={styles.panel} onKeyDown={onKeyDown}>
        <p className={styles.count}>{copy.onboarding.stepOf(index + 1, total)}</p>
        <h2 id={titleId} className={styles.title}>
          {step.title}
        </h2>
        <p id={bodyId} className={styles.body}>
          {step.body}
        </p>
        <div className={styles.actions}>
          <Button onClick={back} disabled={index === 0}>
            {copy.onboarding.back}
          </Button>
          <Button ref={nextButton} variant="primary" onClick={next}>
            {last ? copy.onboarding.finish : copy.onboarding.next}
          </Button>
          <Button variant="ghost" onClick={() => onClose("skipped")}>
            {copy.onboarding.skip}
          </Button>
        </div>
        <div role="status" className="visually-hidden">
          {copy.onboarding.announceStep(index + 1, total, step.title)}
        </div>
      </div>
    </div>
  );
}
