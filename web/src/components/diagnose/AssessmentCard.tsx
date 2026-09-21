import { useEffect, useId, useState, type ReactNode } from "react";
import {
  Loader2,
  CheckCircle2,
  AlertTriangle,
  ShieldCheck,
  Wrench,
  Sparkles,
  HelpCircle,
  FileSearch,
  Search,
  ListChecks,
} from "lucide-react";
import type { Diagnosis, ApplyMutationOutcome } from "../../api/diagnose";
import { Collapse, CollapseChevron } from "@skyhook-io/k8s-ui";
import { Tooltip } from "../ui/Tooltip";
import type {
  InvestigationRootCauseEvidenceResolution,
  InvestigationEvidenceSource,
} from "./investigationEvidence";
import type {
  InvestigationCaseItem,
  InvestigationCaseResolution,
} from "./investigationCase";
import { AgentClaimNote } from "./AgentCase";
import type { InvestigationHealthSignal } from "./investigationState";
import { Badge } from "@skyhook-io/k8s-ui";
import { diagnosisHasStoryShape, storyPlainText } from "./investigationStory";
import { useDisclosureReveal } from "./useDisclosureReveal";
import { FollowupAnswer } from "./ActivityTurn";
import { ApplyOutcomeCard } from "./ApplyDialog";
import {
  type AssessmentCopyRadar,
  CERTAINTY_LABEL,
  STEP_KIND_LABEL,
  assessmentCopyText,
} from "./assessmentCopy";
import { AIMarkdown, CopyButton, tidyFences } from "./AIMarkdown";
import { prettyTool } from "./toolCallLabel";

export function ResultCard({
  diagnosis,
  onApply,
  explanation,
  apply,
  applyOutcome,
  followup,
  section = "full",
  onCheckStatus,
  animate = true,
  showDisclaimer = true,
  coverageLimited = false,
  evidenceConflict = false,
  evidenceConflictExplainedBy,
  compactActions = false,
  assessmentAction,
  assessmentSources,
  actionNotice,
  storyInline = false,
  readOnlyAssessment = false,
  revisedAfter,
  assessmentLimits,
  assessmentCopy,
  healthSignals,
  onRevealSource,
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  explanation?: AssessmentExplanation;
  apply?: boolean;
  applyOutcome?: ApplyMutationOutcome;
  followup?: boolean;
  section?: "full" | "conclusion" | "actions";
  /**
   * Render the story as plain prose inside this card. Findings renders the
   * story itself, with placed cards, so it leaves this off; Activity's
   * read-only copies of earlier assessments turn it on.
   */
  storyInline?: boolean;
  /** An earlier assessment shown for the record: no Apply, labelled as such. */
  readOnlyAssessment?: boolean;
  /** The question that produced this revised assessment, when it replaced an earlier one. */
  revisedAfter?: string;
  /** Reads Radar could not complete for this assessment; listed under Still open. */
  assessmentLimits?: string[];
  assessmentCopy?: Pick<AssessmentCopyRadar, "context" | "receipts">;
  healthSignals?: InvestigationHealthSignal[];
  onRevealSource?: (sourceId: string) => void;
  onCheckStatus?: () => void;
  animate?: boolean;
  /** Additional navigation placed in the assessment action row. */
  assessmentAction?: ReactNode;
  assessmentSources?: ReactNode;
  /** Hide the repeated disclaimer when the enclosing surface provides context. */
  showDisclaimer?: boolean;
  /** Qualifies a healthy assessment when structured evidence is absent or partial. */
  coverageLimited?: boolean;
  /** Marks a healthy agent assessment that conflicts with same-turn Key evidence. */
  evidenceConflict?: boolean;
  /**
   * Titles of the conflicting cards when the agent placed a "not a problem"
   * note on every one of them; the banner then points at the agent's reasons
   * rather than accusing evidence it already addressed.
   */
  evidenceConflictExplainedBy?: string[];
  /** Show only the recommended (or first) action until the user asks for more. */
  compactActions?: boolean;
  actionNotice?: string;
}) {
  // Apply turns report mutation truth, not a diagnosis. The outcome-specific
  // card decides whether that truth is confirmed, failed, or still unknown.
  if (apply)
    return (
      <ApplyOutcomeCard
        diagnosis={diagnosis}
        applyOutcome={applyOutcome}
        onCheckStatus={onCheckStatus}
        animate={animate}
      />
    );
  // A question remains conversational even if the model happens to set a health
  // flag in its structured envelope. Never promote an ordinary answer into an
  // authoritative investigation conclusion.
  if (followup)
    return (
      <>
        <FollowupAnswer diagnosis={diagnosis} animate={animate} />
        {assessmentSources ? (
          <AssessmentSourceDetails>{assessmentSources}</AssessmentSourceDetails>
        ) : null}
      </>
    );
  if (diagnosis.healthy && !diagnosis.rootCause)
    return section === "actions" ? null : (
      <AllClearCard
        diagnosis={diagnosis}
        explanation={explanation}
        animate={animate}
        showDisclaimer={showDisclaimer}
        coverageLimited={coverageLimited}
        evidenceConflict={evidenceConflict}
        evidenceConflictExplainedBy={evidenceConflictExplainedBy}
        assessmentAction={assessmentAction}
        assessmentSources={assessmentSources}
        storyInline={storyInline}
        revisedAfter={revisedAfter}
        assessmentLimits={assessmentLimits}
        assessmentCopy={assessmentCopy}
        healthSignals={healthSignals}
        onRevealSource={onRevealSource}
      />
    );
  // Couldn't-determine is its own honest state — never a confident all-clear, never
  // the alarming root-cause anchor. With typed steps it can still carry the
  // next check, which the actions section renders like any other step.
  if (diagnosis.inconclusive && !diagnosis.rootCause) {
    if (section === "actions")
      return (diagnosis.steps?.length ?? 0) > 0 ? (
        <DiagnosisResult
          diagnosis={diagnosis}
          onApply={readOnlyAssessment ? undefined : onApply}
          section="actions"
          animate={animate}
          showDisclaimer={false}
          compactActions={compactActions}
          actionNotice={actionNotice}
          readOnlyAssessment={readOnlyAssessment}
        />
      ) : null;
    return (
      <>
        <InconclusiveCard
          diagnosis={diagnosis}
          explanation={explanation}
          assessmentSources={assessmentSources}
          assessmentAction={assessmentAction}
          animate={animate}
          storyInline={storyInline}
          revisedAfter={revisedAfter}
          assessmentLimits={assessmentLimits}
          assessmentCopy={assessmentCopy}
        />
        {section === "full" && (diagnosis.steps?.length ?? 0) > 0 ? (
          <DiagnosisResult
            diagnosis={diagnosis}
            onApply={readOnlyAssessment ? undefined : onApply}
            section="actions"
            animate={false}
            showDisclaimer={false}
            compactActions={compactActions}
            actionNotice={actionNotice}
            readOnlyAssessment={readOnlyAssessment}
          />
        ) : null}
      </>
    );
  }
  // A turn with no structured root cause and no remediation (e.g. "looks healthy",
  // or a clarifying question) is not a diagnosis — render it neutrally rather than
  // forcing the alarming root-cause anchor onto a non-problem.
  const structured =
    !!diagnosis.rootCause || (diagnosis.remediation?.length ?? 0) > 0;
  if (!structured)
    return (
      <>
        <FollowupAnswer diagnosis={diagnosis} animate={animate} />
        {assessmentSources ? (
          <AssessmentSourceDetails>{assessmentSources}</AssessmentSourceDetails>
        ) : null}
        {assessmentAction && (
          <div className="mt-2 flex justify-end">{assessmentAction}</div>
        )}
      </>
    );

  return (
    <DiagnosisResult
      diagnosis={diagnosis}
      onApply={readOnlyAssessment ? undefined : onApply}
      explanation={explanation}
      section={section}
      animate={animate}
      showDisclaimer={showDisclaimer}
      compactActions={compactActions}
      assessmentAction={assessmentAction}
      assessmentSources={assessmentSources}
      actionNotice={actionNotice}
      storyInline={storyInline}
      readOnlyAssessment={readOnlyAssessment}
      revisedAfter={revisedAfter}
      assessmentLimits={assessmentLimits}
      assessmentCopy={assessmentCopy}
    />
  );
}

// The word is the agent's; the clause says what kind of Radar evidence sits
// behind it, so a reader knows what the word licenses them to do.
const CERTAINTY_DEFINITION: Record<
  NonNullable<Diagnosis["certainty"]>,
  string
> = {
  established:
    "The agent's word. It found the cause stated outright in Radar's results — a log line, an event, a condition — not just consistent with them.",
  likely:
    "The agent's word. Radar's results fit this explanation, but none of them states the cause outright.",
  suspected:
    "The agent's word. A plausible reading on thin or indirect evidence; treat it as a lead, not a finding.",
};

const HEALTHY_DEFINITION: Record<
  NonNullable<Diagnosis["certainty"]>,
  string
> = {
  established:
    "The agent found no live problem, and Radar's results show that directly — the pod state, logs and events it read.",
  likely:
    "The agent found no live problem; Radar's results fit that reading but do not show it outright.",
  suspected:
    "The agent leans healthy on thin or indirect evidence; treat it as a lead, not a finding.",
};

/**
 * The headline of an assessment that carries the story contract: the
 * agent's plain-language summary with its own word for how sure it is, the
 * technical cause beneath it, and what is still unresolved — which renders
 * whenever the agent listed anything, and says so when it listed nothing,
 * because a persuasive story needs its caveats above the fold, not inside it.
 */
function AssessmentHeadline({
  diagnosis,
  tone,
  revisedAfter,
  limits = [],
  signals,
  flags,
  copy,
}: {
  diagnosis: Diagnosis;
  tone: "cause" | "healthy" | "inconclusive";
  revisedAfter?: string;
  /** Reads Radar could not complete for this assessment, one line each. */
  limits?: string[];
  /** Adverse Radar cards the agent explained, with its position; first in Still open. */
  signals?: { text: string; onReveal?: () => void }[];
  /** Adverse Radar cards nothing explains; shown above a healthy verdict, copied with it. */
  flags?: string[];
  /** Context and receipts for the channel copy. */
  copy?: Pick<AssessmentCopyRadar, "context" | "receipts">;
}) {
  const summary = diagnosis.summary?.trim();
  if (!summary) return null;
  // One block for everything that qualifies the answer: what the agent left
  // open and what Radar could not read. The rest of the page states the
  // answer; this is the only place it argues with itself.
  const unresolved = [
    ...(diagnosis.unresolved ?? []).filter((item) => item.trim()),
    ...limits,
  ];
  const stillOpen = (signals ?? []).length > 0 || unresolved.length > 0;
  return (
    <div data-assessment-headline className="space-y-2">
      <div className="flex items-start justify-between gap-2">
        <p className="text-[15px] font-semibold leading-snug text-theme-text-primary [overflow-wrap:anywhere] [text-wrap:balance]">
          {summary}
        </p>
        <CopyButton
          text={assessmentCopyText(diagnosis, {
            limits,
            signals: (signals ?? []).map((signal) => signal.text),
            flags,
            ...copy,
          })}
          label="Copy for a channel"
        />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-theme-text-secondary">
        {diagnosis.certainty ? (
          <Tooltip
            content={
              tone === "healthy"
                ? HEALTHY_DEFINITION[diagnosis.certainty]
                : CERTAINTY_DEFINITION[diagnosis.certainty]
            }
          >
            <Badge tone="agent" size="sm">
              <Sparkles className="h-2.5 w-2.5 shrink-0" aria-hidden />
              {tone === "healthy"
                ? "Healthy"
                : CERTAINTY_LABEL[diagnosis.certainty]}
            </Badge>
          </Tooltip>
        ) : null}
        {revisedAfter ? (
          <span className="min-w-0 text-theme-text-tertiary">
            Revised after: &ldquo;
            {revisedAfter.length > 110
              ? `${revisedAfter.slice(0, 110).trimEnd()}…`
              : revisedAfter}
            &rdquo;
          </span>
        ) : null}
      </div>
      {tone === "cause" && diagnosis.rootCause ? (
        <AIMarkdown className="text-[13px] leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-secondary">
          {diagnosis.rootCause}
        </AIMarkdown>
      ) : null}
      {stillOpen ? (
        <div
          data-assessment-unresolved
          className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2"
        >
          <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-theme-text-secondary">
            <HelpCircle className="h-3 w-3" aria-hidden />
            {tone === "inconclusive"
              ? "What blocked a conclusion"
              : "Still open"}
          </div>
          <ul className="space-y-0.5 text-xs text-theme-text-primary">
            {unresolved.map((item, index) => (
              <li key={`open-${index}`} className="flex gap-1.5">
                <span aria-hidden className="text-theme-text-tertiary">
                  –
                </span>
                <span className="[overflow-wrap:anywhere]">{item}</span>
              </li>
            ))}
            {(signals ?? []).map((signal, index) => (
              <li
                key={`signal-${index}`}
                className="flex gap-1.5"
                data-health-signal
              >
                <span aria-hidden className="text-theme-text-tertiary">
                  –
                </span>
                {signal.onReveal ? (
                  <button
                    type="button"
                    onClick={signal.onReveal}
                    className="text-left [overflow-wrap:anywhere] hover:text-accent-text"
                  >
                    {signal.text}
                  </button>
                ) : (
                  <span className="[overflow-wrap:anywhere]">
                    {signal.text}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

/**
 * Sources the assessment cites: root-cause links first, then every source an
 * agent item cites. Each row shows the roles the agent gave that source; a
 * claim the pane could not pin to one observation is shown here in full.
 */
export function assessmentSourceRows(
  resolution: InvestigationRootCauseEvidenceResolution | undefined,
  investigationCase: InvestigationCaseResolution | undefined,
): Array<{
  source: InvestigationEvidenceSource;
  items: InvestigationCaseItem[];
}> {
  const rows = new Map<
    string,
    { source: InvestigationEvidenceSource; items: InvestigationCaseItem[] }
  >();
  if (resolution?.status === "linked") {
    for (const link of resolution.links) {
      rows.set(link.source.id, { source: link.source, items: [] });
    }
  }
  for (const item of investigationCase?.items ?? []) {
    const row = rows.get(item.source.id) ?? { source: item.source, items: [] };
    row.items.push(item);
    rows.set(item.source.id, row);
  }
  return [...rows.values()];
}

function joinTitles(titles: string[]): string {
  if (titles.length <= 1) return titles[0] ?? "";
  if (titles.length === 2) return `${titles[0]} and ${titles[1]}`;
  return `${titles.slice(0, -1).join(", ")}, and ${titles[titles.length - 1]}`;
}

/**
 * Provenance for one assessment: the exact tool results it cited, each with
 * its source, and under a source only the agent notes that are not already
 * shown on a card. Notes that live on cards are counted, not repeated; an
 * earlier assessment no longer annotates the Evidence pane, so all of its
 * notes are listed here instead of being lost.
 */
export function AssessmentSources({
  resolution,
  investigationCase,
  unlinkedEvidence = 0,
  evidenceMalformed = false,
  omittedEntries = 0,
  readOnly = false,
  renderedGroupIds,
  onViewSource,
}: {
  resolution?: InvestigationRootCauseEvidenceResolution;
  investigationCase?: InvestigationCaseResolution;
  /**
   * Notes the agent wrote that could not be tied to a Radar result: a
   * reference that named nothing, a role Radar does not know, a sentence over
   * the length limit. Radar does not repair them, because repairing one means
   * deciding what the agent meant, so it says how many were lost instead.
   */
  unlinkedEvidence?: number;
  /**
   * The agent's notes were not a list at all, so none of them could be read
   * and no count describes how many were lost.
   */
  evidenceMalformed?: boolean;
  /**
   * Next steps, open items and ruled-out hypotheses the server left out of
   * the record because the list ran past the page's limit. They were valid;
   * the reader learns the list was longer than what is shown.
   */
  omittedEntries?: number;
  readOnly?: boolean;
  /**
   * Groups the Evidence pane actually rendered. A card-placed note whose card
   * was withheld is shown here instead of being counted as visible elsewhere;
   * without this the note renders in neither place. Omitted by hosts that do
   * not know, which keeps the original counting.
   */
  renderedGroupIds?: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
}) {
  const rows = assessmentSourceRows(resolution, investigationCase);
  if (
    rows.length === 0 &&
    unlinkedEvidence === 0 &&
    !evidenceMalformed &&
    omittedEntries === 0
  )
    return null;
  return (
    <div className="mt-3 border-t border-theme-border/60 pt-2">
      <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
        Sources used for this assessment
        <span className="ml-1.5 font-medium normal-case tracking-normal text-theme-text-tertiary">
          {rows.length}
        </span>
      </h4>
      <ul className="mt-1 divide-y divide-theme-border/50">
        {rows.map(({ source, items }) => {
          const shownOnCard = (item: InvestigationCaseItem) =>
            !!item.claim &&
            item.placement !== "source" &&
            (!renderedGroupIds ||
              (!!item.groupId && renderedGroupIds.has(item.groupId)));
          const onCards = items.filter(shownOnCard).length;
          const notes = items.filter(
            (item) => item.claim && (readOnly || !shownOnCard(item)),
          );
          return (
            <li key={source.id} className="py-1.5">
              <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-x-2 px-2">
                <FileSearch
                  className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
                  aria-hidden
                />
                <div className="min-w-0 text-xs">
                  <div className="font-medium text-theme-text-primary">
                    {prettyTool(source.tool)}
                  </div>
                  <CitedSourceScope source={source} />
                  {!readOnly && onCards > 0 ? (
                    <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
                      {onCards === 1
                        ? "1 agent note on an evidence card"
                        : `${onCards} agent notes on evidence cards`}
                    </div>
                  ) : null}
                </div>
                <button
                  type="button"
                  aria-label={`View ${prettyTool(source.tool)} result used for this assessment`}
                  onClick={() => onViewSource(source.id)}
                  className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
                >
                  View result
                </button>
              </div>
              {notes.length > 0 ? (
                <div
                  className="ml-[1.9rem] mr-2 mt-1.5 space-y-1.5"
                  data-source-placed-claims
                >
                  <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                    {readOnly
                      ? "Notes from this assessment"
                      : "Notes not shown on a card"}
                  </div>
                  {notes.map((item) => (
                    <AgentClaimNote
                      key={item.index}
                      claim={item.claim}
                      role={item.role}
                      subject={
                        item.placement === "source"
                          ? undefined
                          : item.observation?.title
                      }
                      className="border-t-0"
                    />
                  ))}
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
      {evidenceMalformed ? (
        <p className="mt-2 text-[11px] text-theme-text-tertiary">
          The agent&apos;s notes could not be read, so none are shown.
        </p>
      ) : null}
      {unlinkedEvidence > 0 ? (
        <p className="mt-2 text-[11px] text-theme-text-tertiary">
          {unlinkedEvidence === 1
            ? "1 agent note could not be linked to a Radar result and is not shown."
            : `${unlinkedEvidence} agent notes could not be linked to Radar results and are not shown.`}
        </p>
      ) : null}
      {omittedEntries > 0 ? (
        <p className="mt-2 text-[11px] text-theme-text-tertiary">
          {omittedEntries === 1
            ? "1 next step, open item or ruled-out hypothesis went past the page's limit and is not shown."
            : `${omittedEntries} next steps, open items or ruled-out hypotheses went past the page's limits and are not shown.`}
        </p>
      ) : null}
    </div>
  );
}

export function WorkingNotes({ notes }: { notes: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const reveal = useDisclosureReveal<HTMLDivElement>();
  return (
    <div ref={reveal.elementRef} data-turn-working-notes>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => {
          setOpen(!open);
          reveal.revealAfterToggle(!open);
        }}
        className="flex items-center gap-1.5 rounded-md py-1 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        Evidence considered
      </button>
      <div id={id}>
        <Collapse open={open}>
          <AIMarkdown className="py-0.5 text-xs leading-relaxed text-theme-text-tertiary [overflow-wrap:anywhere] [&_li]:text-theme-text-tertiary [&_p]:my-0.5 [&_strong]:font-medium [&_strong]:text-theme-text-secondary">
            {notes}
          </AIMarkdown>
        </Collapse>
      </div>
    </div>
  );
}

function AssessmentSourceDetails({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const reveal = useDisclosureReveal<HTMLDivElement>();
  return (
    <div ref={reveal.elementRef}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => {
          setOpen(!open);
          reveal.revealAfterToggle(!open);
        }}
        className="flex items-center gap-1.5 rounded-md py-2 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        Assessment details
      </button>
      <div id={id}>
        <Collapse open={open}>{children}</Collapse>
      </div>
    </div>
  );
}

function CitedSourceScope({ source }: { source: InvestigationEvidenceSource }) {
  let input: unknown;
  try {
    input = JSON.parse(source.args ?? "");
  } catch {
    return null;
  }
  if (!input || typeof input !== "object" || Array.isArray(input)) return null;
  const args = input as Record<string, unknown>;
  const fields = ["query", "kind", "namespace", "name", "filter"].flatMap(
    (key) =>
      typeof args[key] === "string" && args[key].trim()
        ? [`${key === "query" ? "" : `${key}: `}${args[key]}`]
        : [],
  );
  return fields.length > 0 ? (
    <span className="mt-0.5 block break-words text-theme-text-secondary [overflow-wrap:anywhere]">
      {fields.join(" · ")}
    </span>
  ) : null;
}

export type AssessmentExplanation = {
  status: "idle" | "running" | "done" | "error";
  text?: string;
  error?: string;
  onGenerate?: () => void;
  openRequest?: number;
};

function AssessmentDetails({
  diagnosis,
  explanation,
  assessmentAction,
  assessmentSources,
  showAnalysisDisclosure = false,
  showConfidence = false,
  analysisText = diagnosis.report,
}: {
  diagnosis: Diagnosis;
  explanation?: AssessmentExplanation;
  assessmentAction?: ReactNode;
  assessmentSources?: ReactNode;
  showAnalysisDisclosure?: boolean;
  showConfidence?: boolean;
  analysisText?: string;
}) {
  const [detail, setDetail] = useState<"analysis" | "explanation" | null>(
    explanation?.status === "running" ? "explanation" : null,
  );
  const showAnalysis = detail === "analysis";
  const analysisReveal = useDisclosureReveal<HTMLDivElement>();
  const { elementRef: analysisElementRef, revealAfterToggle: revealAnalysis } =
    analysisReveal;
  useEffect(() => {
    if (explanation?.openRequest) {
      setDetail("explanation");
      revealAnalysis(true);
    }
  }, [explanation?.openRequest, revealAnalysis]);
  useEffect(() => {
    if (
      detail === "explanation" &&
      (explanation?.status === "done" || explanation?.status === "error")
    ) {
      const element = analysisElementRef.current;
      const scroller = element?.closest("[data-investigation-findings-scroll]");
      if (element && scroller) {
        const top = element.getBoundingClientRect().top;
        const viewport = scroller.getBoundingClientRect();
        if (top >= viewport.top && top < viewport.bottom) revealAnalysis(true);
      }
    }
  }, [explanation?.status, detail, analysisElementRef, revealAnalysis]);
  const analysisId = useId();
  return (
    <>
      {/* Full analysis — the agent's detailed evidence, on demand. Under the
          story contract Findings renders the story itself, so only the
          explanation and sources remain here. */}
      {((showAnalysisDisclosure && diagnosis.report) ||
        (showAnalysisDisclosure && diagnosis.confidence != null) ||
        explanation ||
        assessmentAction ||
        assessmentSources) && (
        <div>
          <div
            className="flex flex-wrap items-center gap-x-3 gap-y-1 pt-2"
            data-assessment-actions
          >
            {((showAnalysisDisclosure && diagnosis.report) ||
              (showAnalysisDisclosure && diagnosis.confidence != null) ||
              assessmentSources) && (
              <button
                type="button"
                aria-expanded={showAnalysis}
                aria-controls={`${analysisId}-analysis`}
                onClick={() => {
                  setDetail(showAnalysis ? null : "analysis");
                  analysisReveal.revealAfterToggle(!showAnalysis);
                }}
                className="flex items-center gap-1.5 rounded-md py-2 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
              >
                <CollapseChevron open={showAnalysis} className="h-3.5 w-3.5" />
                {showAnalysisDisclosure && diagnosis.report
                  ? "Full analysis"
                  : "Assessment details"}
              </button>
            )}
            {explanation && (
              <Tooltip
                content={
                  explanation.status === "idle"
                    ? explanation.onGenerate
                      ? "Ask the agent to explain this assessment and its proposed next steps in plain language."
                      : "Wait for the current agent request to finish before requesting an explanation."
                    : explanation.status === "running"
                      ? "The agent is preparing an explanation. You can close this and return while it runs."
                      : explanation.status === "error"
                        ? "View the explanation error and retry when the agent is available."
                        : "Show the saved plain-language explanation. No new request is needed."
                }
              >
                <button
                  type="button"
                  aria-expanded={detail === "explanation"}
                  aria-controls={`${analysisId}-explanation`}
                  disabled={
                    explanation.status === "idle" && !explanation.onGenerate
                  }
                  onClick={() => {
                    const open = detail !== "explanation";
                    setDetail(open ? "explanation" : null);
                    analysisReveal.revealAfterToggle(open);
                    if (open && explanation.status === "idle")
                      explanation.onGenerate?.();
                  }}
                  className="inline-flex items-center gap-1 rounded-md px-2 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:opacity-50"
                >
                  <HelpCircle className="h-3 w-3" />
                  Explain simply
                </button>
              </Tooltip>
            )}
            {assessmentAction && (
              <div className="ml-auto">{assessmentAction}</div>
            )}
          </div>
          <div id={analysisId} ref={analysisReveal.elementRef}>
            <div id={`${analysisId}-analysis`}>
              <Collapse open={detail === "analysis"} mountLazily>
                <div className="border-t border-theme-border/60 px-3 py-2">
                  {showAnalysisDisclosure ? (
                    <>
                      {showConfidence && (
                        <p className="mb-2 text-xs text-theme-text-tertiary">
                          Agent confidence:{" "}
                          {diagnosis.confidence != null
                            ? confidenceLabel(diagnosis.confidence)
                            : "not stated"}
                          {diagnosis.confidence != null
                            ? " · self-reported"
                            : ""}
                        </p>
                      )}
                      <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_h2:first-child]:mt-0 [&_h2]:mb-1.5 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-secondary [&_p]:my-1.5 [&_p]:text-theme-text-secondary">
                        {analysisText}
                      </AIMarkdown>
                    </>
                  ) : null}
                  {assessmentSources}
                </div>
              </Collapse>
            </div>
            <div id={`${analysisId}-explanation`}>
              <Collapse open={detail === "explanation"} mountLazily>
                <div className="border-t border-theme-border/60 px-3 py-2">
                  {explanation?.status === "running" ? (
                    <div
                      role="status"
                      className="flex items-center gap-2 py-2 text-sm text-theme-text-secondary"
                    >
                      <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" />
                      Explaining this assessment…
                    </div>
                  ) : explanation?.status === "done" ? (
                    <div className="flex items-start gap-2">
                      <AIMarkdown className="min-w-0 flex-1 text-sm text-theme-text-secondary [overflow-wrap:anywhere]">
                        {explanation.text || ""}
                      </AIMarkdown>
                      <CopyButton
                        text={explanation.text || ""}
                        label="Copy explanation"
                      />
                    </div>
                  ) : explanation?.status === "error" ? (
                    <div
                      role="alert"
                      className="flex flex-wrap items-center gap-2 py-2 text-sm text-theme-text-secondary"
                    >
                      <span>
                        {explanation.error ||
                          "The agent did not return an explanation."}
                      </span>
                      {explanation.onGenerate && (
                        <button
                          type="button"
                          onClick={explanation.onGenerate}
                          className="rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover"
                        >
                          Try again
                        </button>
                      )}
                    </div>
                  ) : null}
                </div>
              </Collapse>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function DiagnosisResult({
  diagnosis,
  onApply,
  explanation,
  section = "full",
  animate,
  showDisclaimer,
  compactActions,
  assessmentAction,
  assessmentSources,
  actionNotice,
  storyInline = false,
  readOnlyAssessment = false,
  revisedAfter,
  assessmentLimits,
  assessmentCopy,
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  explanation?: AssessmentExplanation;
  section?: "full" | "conclusion" | "actions";
  animate: boolean;
  showDisclaimer: boolean;
  compactActions: boolean;
  assessmentAction?: ReactNode;
  assessmentSources?: ReactNode;
  actionNotice?: string;
  storyInline?: boolean;
  readOnlyAssessment?: boolean;
  revisedAfter?: string;
  /** Reads Radar could not complete for this assessment; listed under Still open. */
  assessmentLimits?: string[];
  assessmentCopy?: Pick<AssessmentCopyRadar, "context" | "receipts">;
}) {
  // The story contract: a summary headline above, the story rendered by the
  // host (or inline as plain prose), typed steps below.
  const storyShape = diagnosisHasStoryShape(diagnosis);
  const analysisText =
    storyShape && storyInline
      ? storyPlainText(diagnosis.report)
      : diagnosis.report;
  const showAnalysisDisclosure = !storyShape || storyInline;
  const [showAllSteps, setShowAllSteps] = useState(false);
  const stepsId = useId();
  const stepsReveal = useDisclosureReveal<HTMLDivElement>();
  // Only a real structured cause anchors the amber card; the full prose lives in
  // "Full analysis" (never relabel the report as a causal assessment).
  const rootCause = diagnosis.rootCause;
  const remediation = diagnosis.remediation || [];
  const steps = diagnosis.steps ?? [];
  const typedSteps = steps.length === remediation.length && steps.length > 0;
  const hasRemediation = remediation.length > 0;
  const recIdx = diagnosis.recommendedIndex;
  const recValid =
    recIdx != null &&
    recIdx >= 1 &&
    recIdx <= remediation.length &&
    (!typedSteps || steps[recIdx - 1].kind === "mitigate");
  // Apply is offered ONLY when the agent pointed at a safe step (recommended_index).
  // When it returns 0 / none ("needs human judgement"), we honor that and don't
  // offer one-click apply — the steps stay copy-only with a note.
  const canApply = !!onApply && recValid;
  const showConclusion = section !== "actions";
  const showActions = section !== "conclusion";
  const remediationEntries = remediation.map((text, index) => ({
    text,
    index,
  }));
  const primaryActionIndex = recValid ? recIdx! - 1 : 0;
  const hiddenStepCount = remediationEntries.length - 1;
  const renderRemediationStep = ({
    text: r,
    index: i,
  }: {
    text: string;
    index: number;
  }) => {
    const isRec = recValid && i === recIdx! - 1;
    const step = typedSteps ? steps[i] : undefined;
    const commands = remediationCommands(r);
    return (
      <div
        key={i}
        data-step-kind={step?.kind}
        className={
          isRec ? "rounded-lg border border-accent/40 bg-accent/5 p-2.5" : ""
        }
      >
        <div className="flex items-start gap-2">
          <span
            className={`mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[10px] ${
              isRec
                ? "bg-accent/20 text-accent"
                : "bg-theme-base text-theme-text-tertiary"
            }`}
          >
            {step ? (
              step.kind === "mitigate" ? (
                <Wrench className="h-2.5 w-2.5" aria-hidden />
              ) : step.kind === "verify" ? (
                <ListChecks className="h-2.5 w-2.5" aria-hidden />
              ) : (
                <Search className="h-2.5 w-2.5" aria-hidden />
              )
            ) : (
              i + 1
            )}
          </span>
          <div className="min-w-0 flex-1">
            {/* Labels, then the action cluster pushed to the row's end, then
                the reason on its own line — so the step text below spans the
                card instead of wrapping beside the buttons. */}
            <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-0.5">
              {(isRec || step) && (
                <>
                  {step ? (
                    <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                      {STEP_KIND_LABEL[step.kind]}
                    </span>
                  ) : null}
                  {isRec && (
                    <span className="flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wide text-accent">
                      <Sparkles className="h-3 w-3" />
                      Recommended
                    </span>
                  )}
                </>
              )}
              <div className="ml-auto flex shrink-0 items-center gap-0.5">
                {canApply && isRec && (
                  <button
                    onClick={() => onApply!(r)}
                    className="inline-flex items-center gap-1 rounded-md border border-accent/40 bg-accent/10 px-2 py-1 text-xs font-medium text-accent transition-colors hover:bg-accent/20"
                  >
                    <Wrench className="h-3 w-3" />
                    Apply…
                  </button>
                )}
              </div>
            </div>
            {/* The condition and the reason are read before the command is
                copied, so they precede it at a size that is meant to be read. */}
            {step?.precondition ? (
              <p
                data-step-precondition
                className="mb-1.5 text-[13px] leading-snug text-warning-text"
              >
                Only if {step.precondition.replace(/^(if|when|once)\s+/i, "")}
              </p>
            ) : null}
            {isRec && diagnosis.recommendedReason && (
              <p
                data-recommended-reason
                className="mb-1.5 text-[13px] leading-snug text-theme-text-secondary"
              >
                {diagnosis.recommendedReason}
              </p>
            )}
            <AIMarkdown
              className="max-w-[100ch] text-sm [overflow-wrap:anywhere] [&_p]:my-0 [&_pre]:my-1.5"
              codeActions={(code) => {
                const command = code.trim();
                if (!commands.includes(command)) return null;
                return (
                  <CopyButton
                    text={command}
                    label={`Copy command from step ${i + 1}`}
                  />
                );
              }}
            >
              {r}
            </AIMarkdown>
          </div>
        </div>
      </div>
    );
  };
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      {showConclusion && storyShape && (
        <div
          className={
            section === "conclusion"
              ? ""
              : "rounded-lg border border-theme-border bg-theme-surface p-3"
          }
        >
          {readOnlyAssessment ? (
            <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
              Earlier assessment
            </div>
          ) : null}
          <AssessmentHeadline
            diagnosis={diagnosis}
            tone="cause"
            revisedAfter={revisedAfter}
            limits={assessmentLimits}
            copy={assessmentCopy}
          />
        </div>
      )}
      {/* Likely cause — agent-authored, visually prominent without claiming proof. */}
      {showConclusion && !storyShape && rootCause && (
        <div
          className={
            section === "conclusion"
              ? "grid grid-cols-[minmax(0,1fr)_auto] items-start gap-2"
              : "rounded-lg border border-theme-border bg-theme-surface p-3"
          }
        >
          <div
            className={
              section === "conclusion"
                ? "col-start-2 row-start-1"
                : "mb-1 flex items-center justify-between gap-2"
            }
          >
            {section !== "conclusion" && (
              <div className="text-xs font-medium text-theme-text-secondary">
                Likely cause
              </div>
            )}
            <div className="flex items-center gap-2">
              <CopyButton text={rootCause} label="Copy likely cause" />
            </div>
          </div>
          <AIMarkdown
            className={`${section === "conclusion" ? "col-start-1 row-start-1 max-w-[100ch]" : "max-w-prose"} text-sm leading-relaxed text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-primary`}
          >
            {rootCause}
          </AIMarkdown>
        </div>
      )}

      {/* Remediation — every step is copyable. Only the explicitly recommended
          step can be applied, and only when the caller enables apply. */}
      {showActions && hasRemediation && (
        <div
          ref={stepsReveal.elementRef}
          className={
            section === "actions"
              ? "space-y-2"
              : "rounded-lg border border-theme-border bg-theme-surface p-3"
          }
        >
          {section !== "actions" && (
            <div className="mb-2 flex items-center gap-1.5 text-xs font-medium text-theme-text-secondary">
              <Wrench className="h-3.5 w-3.5 text-accent" />
              {typedSteps ? "Next steps" : "Remediation"}
            </div>
          )}
          {actionNotice && (
            <p className="text-xs text-theme-text-secondary">{actionNotice}</p>
          )}
          <div id={stepsId}>
            <ol>
              {remediationEntries.map((entry) => {
                const expanded =
                  !compactActions ||
                  showAllSteps ||
                  entry.index === primaryActionIndex;
                const step = typedSteps ? steps[entry.index] : undefined;
                return (
                  <li key={entry.index} value={entry.index + 1}>
                    <Collapse open={expanded} mountLazily>
                      <div className="pt-2">{renderRemediationStep(entry)}</div>
                    </Collapse>
                    {/* A folded step still shows what it is: its kind and
                        first line, one row each, so the alternatives are
                        readable before anyone opens them. */}
                    {!expanded ? (
                      <button
                        type="button"
                        data-step-folded={step?.kind ?? "step"}
                        onClick={() => {
                          setShowAllSteps(true);
                          stepsReveal.revealAfterToggle(true);
                        }}
                        className="mt-1.5 flex w-full min-w-0 items-baseline gap-2 rounded-md px-2 py-1 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
                      >
                        {step ? (
                          <span className="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                            {STEP_KIND_LABEL[step.kind]}
                          </span>
                        ) : (
                          <span className="shrink-0 text-[10px] font-semibold text-theme-text-tertiary">
                            {entry.index + 1}
                          </span>
                        )}
                        <span className="min-w-0 flex-1 truncate">
                          {remediationHeadline(entry.text)}
                        </span>
                      </button>
                    ) : null}
                  </li>
                );
              })}
            </ol>
          </div>
          {compactActions && remediation.length > 1 ? (
            <button
              type="button"
              aria-expanded={showAllSteps}
              aria-controls={stepsId}
              onClick={() => {
                setShowAllSteps(!showAllSteps);
                stepsReveal.revealAfterToggle(!showAllSteps);
              }}
              className="mt-2 inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-[11px] font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
            >
              <CollapseChevron open={showAllSteps} className="h-3.5 w-3.5" />
              {showAllSteps
                ? recValid
                  ? "Show only recommended step"
                  : "Show only first step"
                : hiddenStepCount === 1
                  ? "Expand the other step"
                  : "Expand all steps"}
            </button>
          ) : null}
          {!actionNotice && !recValid && (
            <p className="mt-2 flex items-start gap-1.5 text-[11px] leading-snug text-theme-text-tertiary">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
              No one-click fix is available. Review these steps and apply them
              manually, or ask the agent to continue.
            </p>
          )}
        </div>
      )}

      {showConclusion && (
        <AssessmentDetails
          diagnosis={diagnosis}
          explanation={explanation}
          assessmentAction={assessmentAction}
          assessmentSources={assessmentSources}
          showAnalysisDisclosure={showAnalysisDisclosure}
          showConfidence
          analysisText={analysisText}
        />
      )}

      {showConclusion && showDisclaimer && (
        <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
          <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
          <span>AI-generated — review before applying</span>
        </div>
      )}
    </div>
  );
}

/** The banner line and the clipboard line for one adverse Radar card must be the same words. */
function healthFlagSentence(flag: {
  status: string;
  title: string;
  role?: string;
}): string {
  return flag.status === "contradiction"
    ? `The agent calls ${flag.title} a ${flag.role} and still reports healthy`
    : `Radar flagged ${flag.title} · no explanation is linked to it`;
}

export function AllClearCard({
  diagnosis,
  explanation,
  animate,
  showDisclaimer,
  coverageLimited,
  evidenceConflict,
  evidenceConflictExplainedBy,
  assessmentAction,
  assessmentSources,
  storyInline = false,
  revisedAfter,
  assessmentLimits,
  assessmentCopy,
  healthSignals,
  onRevealSource,
}: {
  diagnosis: Diagnosis;
  animate: boolean;
  showDisclaimer: boolean;
  coverageLimited: boolean;
  explanation?: AssessmentExplanation;
  evidenceConflict: boolean;
  evidenceConflictExplainedBy?: string[];
  assessmentAction?: ReactNode;
  assessmentSources?: ReactNode;
  storyInline?: boolean;
  revisedAfter?: string;
  /** Reads Radar could not complete for this assessment; listed under Still open. */
  assessmentLimits?: string[];
  assessmentCopy?: Pick<AssessmentCopyRadar, "context" | "receipts">;
  /** Adverse Radar cards with the agent's position on each (story shape). */
  healthSignals?: InvestigationHealthSignal[];
  onRevealSource?: (sourceId: string) => void;
}) {
  const storyShape = diagnosisHasStoryShape(diagnosis);
  const report =
    (storyShape && !storyInline ? "" : storyPlainText(diagnosis.report)) ||
    (storyShape
      ? ""
      : "The agent did not identify a problem in the evidence it checked.");
  const detailed = report.length > 320 || report.split("\n").length > 2;
  const summary = storyShape
    ? ""
    : detailed
      ? "The agent found no active problem in the evidence it reviewed."
      : report;
  const explained =
    evidenceConflict &&
    !!evidenceConflictExplainedBy &&
    evidenceConflictExplainedBy.length > 0;
  const unexplainedConflict = evidenceConflict && !explained;
  // Story shape: the summary and the certainty word are the verdict; adverse
  // Radar cards the agent explained become lines in Still open, and only a
  // card the agent's verdict never addressed (or contradicts) keeps a header
  // that names it — so the reader knows exactly how it relates to the answer.
  const signals = healthSignals ?? [];
  const flagged = signals.filter(
    (signal) =>
      signal.status === "unaddressed" || signal.status === "contradiction",
  );
  const stillOpenSignals = signals
    .filter(
      (signal) => signal.status === "explained" || signal.status === "related",
    )
    .map((signal) => ({
      text:
        signal.status === "explained"
          ? `Radar flagged ${signal.title} · the agent looked at it and reads it as not a live problem: ${signal.claim}`
          : `Radar flagged ${signal.title} · the agent reads it as related, but not what matters here: ${signal.claim}`,
      onReveal:
        signal.sourceId && onRevealSource
          ? () => onRevealSource(signal.sourceId!)
          : undefined,
    }));
  // The analysis disclosure (Activity's read-only copy of an earlier healthy
  // assessment carries its story inline as plain prose), the action slot and
  // the disclaimer are the same in both shapes.
  const trailing = (
    <>
      <AssessmentDetails
        diagnosis={diagnosis}
        explanation={explanation}
        assessmentAction={assessmentAction}
        assessmentSources={assessmentSources}
        showAnalysisDisclosure={detailed}
        analysisText={report}
      />
      {showDisclaimer ? (
        <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
          <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
          <span>AI-generated — verify if symptoms persist</span>
        </div>
      ) : null}
    </>
  );
  if (storyShape) {
    return (
      <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
        {flagged.map((flag) => (
          <div
            key={`${flag.groupId ?? flag.title}-${flag.status}`}
            data-health-flag={flag.status}
            className="rounded-md border border-amber-500/40 bg-amber-500/5 px-2.5 py-2 text-xs text-theme-text-primary"
          >
            <div className="flex items-center gap-1.5 font-semibold text-amber-500">
              <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
              {healthFlagSentence(flag)}
            </div>
            <p className="mt-1 text-theme-text-secondary">
              {flag.status === "contradiction"
                ? "Read the card before treating this as an all-clear."
                : "It may be unrelated to what you asked, or missed. Open it before treating this as an all-clear."}
              {flag.sourceId && onRevealSource ? (
                <>
                  {" "}
                  <button
                    type="button"
                    onClick={() => onRevealSource(flag.sourceId!)}
                    className="font-medium text-accent-text hover:underline"
                  >
                    View the card
                  </button>
                </>
              ) : null}
            </p>
          </div>
        ))}
        <AssessmentHeadline
          diagnosis={diagnosis}
          tone="healthy"
          revisedAfter={revisedAfter}
          flags={flagged.map(healthFlagSentence)}
          limits={assessmentLimits}
          copy={assessmentCopy}
          signals={stillOpenSignals}
        />
        {trailing}
      </div>
    );
  }
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div
        className={`rounded-lg border p-3 ${
          unexplainedConflict || explained
            ? "border-amber-500/40 bg-amber-500/5"
            : coverageLimited
              ? "border-amber-500/30 bg-amber-500/5"
              : "border-emerald-500/30 bg-emerald-500/5"
        }`}
      >
        <div className="mb-1 flex items-center justify-between gap-2">
          <div
            className={`flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide ${
              unexplainedConflict || explained || coverageLimited
                ? "text-amber-500"
                : "text-emerald-500"
            }`}
          >
            {unexplainedConflict || explained || coverageLimited ? (
              <AlertTriangle className="h-3.5 w-3.5" />
            ) : (
              <CheckCircle2 className="h-3.5 w-3.5" />
            )}
            {unexplainedConflict
              ? "Assessment conflicts with captured evidence"
              : explained
                ? "Agent reports no active problem; adverse evidence remains"
                : coverageLimited
                  ? "No problem identified in available evidence"
                  : "No problem found in checked evidence"}
          </div>
          <CopyButton text={report} label="Copy assessment" />
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {summary}
        </AIMarkdown>
        {unexplainedConflict ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Radar also captured evidence of an active problem. Review that
            evidence before treating the agent&apos;s conclusion as an
            all-clear.
          </p>
        ) : explained ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Radar captured evidence of an active problem. The agent explains its
            interpretation in the note on{" "}
            {joinTitles(evidenceConflictExplainedBy!)}.
            {coverageLimited
              ? " Evidence coverage is also limited — review the limitations in Evidence."
              : ""}
          </p>
        ) : coverageLimited ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Evidence coverage is limited. Review the limitations in Evidence
            before treating this as an all-clear.
          </p>
        ) : null}
      </div>
      {trailing}
    </div>
  );
}

// The agent investigated but couldn't determine an answer. A distinct, honest
// state — neutral (not the alarming amber root cause, not the reassuring emerald
// all-clear) — so "I couldn't tell" never reads as "you're fine."
export function InconclusiveCard({
  diagnosis,
  explanation,
  assessmentSources,
  assessmentAction,
  animate,
  storyInline = false,
  revisedAfter,
  assessmentLimits,
  assessmentCopy,
}: {
  diagnosis: Diagnosis;
  animate: boolean;
  storyInline?: boolean;
  explanation?: AssessmentExplanation;
  revisedAfter?: string;
  assessmentSources?: ReactNode;
  assessmentAction?: ReactNode;
  /** Reads Radar could not complete for this assessment; listed under Still open. */
  assessmentLimits?: string[];
  assessmentCopy?: Pick<AssessmentCopyRadar, "context" | "receipts">;
}) {
  const storyShape = diagnosisHasStoryShape(diagnosis);
  const text =
    (storyShape && !storyInline ? "" : storyPlainText(diagnosis.report)) ||
    (storyShape
      ? ""
      : "The investigation couldn't reach a clear conclusion — some information was unavailable or the evidence was ambiguous.");
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div className="rounded-lg border border-theme-border bg-theme-elevated p-3">
        <div className="mb-1 flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
            <HelpCircle className="h-3.5 w-3.5" />
            Couldn&apos;t determine
          </div>
          {storyShape ? null : (
            <CopyButton text={text} label="Copy assessment" />
          )}
        </div>
        {storyShape ? (
          <AssessmentHeadline
            diagnosis={diagnosis}
            tone="inconclusive"
            revisedAfter={revisedAfter}
            limits={assessmentLimits}
            copy={assessmentCopy}
          />
        ) : null}
        {text ? (
          <AIMarkdown className="mt-2 text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
            {text}
          </AIMarkdown>
        ) : null}
      </div>
      <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
        <span>
          Try a follow-up with more context, or investigate again after
          addressing any errors shown in Activity.
        </span>
      </div>
      <AssessmentDetails
        diagnosis={diagnosis}
        explanation={explanation}
        assessmentSources={assessmentSources}
        assessmentAction={assessmentAction}
      />
    </div>
  );
}

const COMMAND_BINARIES = new Set([
  "kubectl",
  "helm",
  "argocd",
  "flux",
  "kustomize",
  "docker",
  "gcloud",
  "aws",
  "az",
  "mongosh",
  "psql",
  "redis-cli",
  "curl",
  "git",
  "istioctl",
  "velero",
  "cilium",
  "calicoctl",
  "terraform",
  "kn",
  "oc",
  "k9s",
  "skyhook",
]);

/** The first sentence of a step, without its markdown, for a one-line row. */
export function remediationHeadline(step: string): string {
  const firstLine =
    step
      .split(/\r?\n/)
      .map((line) => line.trim())
      .find((line) => line && !line.startsWith("```")) ?? "";
  const sentence = firstLine.split(/(?<=[.!?:])\s/)[0] ?? firstLine;
  // A step written as "Confirm recovery:" expects its command to follow; as a
  // folded title the colon dangles.
  return sentence.replace(/`/g, "").replace(/\*\*/g, "").replace(/:$/, "");
}

/**
 * The commands inside a remediation step, in order: every fenced block and
 * every inline code span that reads as a shell invocation. A step's prose is
 * never worth copying; a command is. The prompt asks the agent to wrap
 * commands in backticks, so this is the seam to read them from.
 */
export function remediationCommands(step: string): string[] {
  const commands: string[] = [];
  const normalized = tidyFences(step);
  const code = /```[a-zA-Z]*\n([\s\S]*?)```|`([^`\n]+)`/g;
  let match: RegExpExecArray | null;
  while ((match = code.exec(normalized))) {
    if (match[1] !== undefined) {
      const body = match[1].trim();
      if (body) commands.push(body);
      continue;
    }
    const span = match[2].trim();
    if (looksLikeCommand(span)) commands.push(span);
  }
  return commands;
}

function looksLikeCommand(span: string): boolean {
  if (!/\s/.test(span)) return false;
  const first = span.split(/\s+/)[0];
  if (COMMAND_BINARIES.has(first)) return true;
  // An unknown binary still reads as a command when it takes flags.
  return /^[a-z][a-z0-9._-]*$/.test(first) && /(^|\s)--?[a-zA-Z]/.test(span);
}

// A coarse band, not a precise %: a two-sig-fig confidence on an LLM judgement
// reads as calibrated when it isn't.
function confidenceLabel(c: number): string {
  if (c >= 0.8) return "High";
  if (c >= 0.5) return "Medium";
  return "Low";
}
