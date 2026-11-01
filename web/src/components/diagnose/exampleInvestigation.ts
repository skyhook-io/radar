// A finished investigation bundled with Radar, so someone who has never run one
// can see what it produces before installing an agent or consenting to
// anything. It is a real run from a test cluster with its identifying values
// replaced; it replays through the same view as a saved run, from this bundle
// rather than the server, so it works against any Radar a host embeds.
import { createContext, useContext } from "react";
import type {
  DiagnoseStreamEvent,
  RunSummary,
  SubscribeHandlers,
} from "../../api/diagnose";

// Never issued by a server: Radar's run ids are `run-<hex>`.
export const EXAMPLE_INVESTIGATION_ID = "example";

export function isExampleInvestigation(id: string | null | undefined): boolean {
  return id === EXAMPLE_INVESTIGATION_ID;
}

// Offered only until the user has investigations of their own. On the setup
// step no history is fetched, and with no agent there is none of theirs to show.
export function exampleInvestigationOffered(input: {
  runs: number;
  runsLoaded: boolean;
  setupPending: boolean;
}): boolean {
  return input.runs === 0 && (input.runsLoaded || input.setupPending);
}

export const EXAMPLE_INVESTIGATION_RUN: RunSummary = {
  id: EXAMPLE_INVESTIGATION_ID,
  kind: "Deployment",
  group: "apps",
  namespace: "guestbook",
  name: "guestbook-ui",
  context: "sample-cluster",
  agent: "claude",
  profile: "safeguarded",
  managedBy: "Argo CD",
  health: {
    health: "healthy",
    issueCount: 1,
    highestSeverity: "critical",
    topReason: "ErrImagePull",
    issues: [
      {
        severity: "critical",
        reason: "ErrImagePull",
        message:
          'rpc error: code = NotFound desc = failed to pull and unpack image "gcr.io/google-samples/gb-frontend:v99-missing": failed to resolve reference "gcr.io/google-samples/gb-frontend:v99-missing": gcr.io/google-samples/gb-fr…',
      },
    ],
    auditCount: 11,
    auditSeverity: "high",
    topFinding: "privilegeEscalation",
    auditFindings: [
      {
        severity: "high",
        reason: "privilegeEscalation",
        message: 'Container "guestbook-ui" allows privilege escalation',
      },
      {
        severity: "medium",
        reason: "automountServiceAccountToken",
        message: "Service account token is auto-mounted",
      },
    ],
  },
  status: "done",
  preview:
    "A direct edit set guestbook-ui to a nonexistent image tag; the new pod can't start and the rollout is stuck on the old pod.",
  createdAt: "2026-10-08T10:45:35Z",
  updatedAt: "2026-10-08T10:46:41Z",
  canContinue: false,
};

// Replays the bundled transcript with the same handler contract as
// subscribeRun: replay start, every event with its sequence, then the replay
// boundary. Loaded on demand so the transcript stays out of the main bundle.
export function replayExampleInvestigation(
  handlers: SubscribeHandlers,
): () => void {
  let cancelled = false;
  void import("./exampleInvestigation.events.json").then(
    ({ default: events }) => {
      if (cancelled) return;
      handlers.onReplayStart?.();
      (events as unknown as DiagnoseStreamEvent[]).forEach((event, index) => {
        if (!cancelled) handlers.onEvent(event, index + 1);
      });
      if (!cancelled) handlers.onEvent({ type: "replay_complete" });
    },
    () => {
      if (!cancelled) handlers.onClosed?.("unavailable");
    },
  );
  return () => {
    cancelled = true;
  };
}

const ExampleInvestigationContext = createContext(false);
export const ExampleInvestigationProvider =
  ExampleInvestigationContext.Provider;

// Copy buttons read this: the example is for looking at, and a copied
// command or verdict would carry sample values somewhere they could be acted on.
export function useInExampleInvestigation(): boolean {
  return useContext(ExampleInvestigationContext);
}
