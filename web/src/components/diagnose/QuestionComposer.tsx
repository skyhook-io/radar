// The free-form way into an investigation: the engineer says what they are
// seeing, where to start and since when, and optionally pins the resource it
// is about. It starts the same investigation, with the same Findings page, as
// Investigate on a resource; it is not a chat.
import { useEffect, useMemo, useRef, useState } from "react";
import {
  AlertOctagon,
  AlertTriangle,
  ArrowRight,
  AtSign,
  CornerDownLeft,
  X,
} from "lucide-react";
import {
  Collapse,
  ISSUE_SEVERITY_LABEL,
  ISSUE_SEVERITY_TEXT_CLASS,
} from "@skyhook-io/k8s-ui";

import {
  useContexts,
  useIssues,
  useNamespaceScope,
  useResourceIssues,
} from "../../api/client";
import { kindToPluralWithGroup } from "../../utils/navigation";
import { useDiagnose } from "./DiagnoseContext";
import {
  suggestionsForPin,
  suggestionsFromIssues,
  type QuestionPin,
  type QuestionSuggestion,
} from "./questionSuggestions";
import { ResourcePicker } from "./ResourcePicker";
import { Tooltip } from "../ui/Tooltip";
import { formatInvestigationTarget } from "./target";

export const LOOK_BACKS = ["15m", "1h", "6h", "24h"] as const;
const DEFAULT_LOOK_BACK = "1h";
const MAX_START_NAMESPACES = 20;
// Radar refuses a longer question; a pasted or handed-over one can arrive
// past the box's own limit.
const MAX_QUESTION_LENGTH = 2000;

// Chips state what the question carries; actions change it. They must not
// look alike, or a scope chip reads as a menu and an action as a value.
const chipClass =
  "inline-flex max-w-full items-center gap-1 rounded-md border border-theme-border bg-theme-elevated px-2 py-0.5 text-xs text-theme-text-secondary";
const actionClass =
  "inline-flex items-center gap-1 rounded-md border border-dashed border-theme-border px-2 py-0.5 text-xs text-theme-text-tertiary hover:border-theme-text-tertiary hover:text-theme-text-primary";

export function QuestionComposer({
  onBrowseIssues,
}: {
  onBrowseIssues?: () => void;
}) {
  const { openInvestigation, agentLabel, questionDraft, saveQuestionDraft } =
    useDiagnose();
  // The draft outlives this component: it starts from it, and gives back
  // whatever is unsent when it unmounts (opening a run, a resource).
  const [text, setText] = useState(questionDraft?.text ?? "");
  const [pin, setPin] = useState<QuestionPin | null>(
    questionDraft?.pin ?? null,
  );
  const [lookBack, setLookBack] = useState<string>(
    questionDraft?.lookBack ?? DEFAULT_LOOK_BACK,
  );
  // A handed-over question keeps when it was first asked until it is edited.
  const [inherited, setInherited] = useState(
    questionDraft?.askedAt && questionDraft.text
      ? { text: questionDraft.text.trim(), askedAt: questionDraft.askedAt }
      : null,
  );
  const [pickerOpen, setPickerOpen] = useState(!!questionDraft?.pickResource);
  const textRef = useRef<HTMLTextAreaElement>(null);
  const pickerInputRef = useRef<HTMLInputElement>(null);
  // Focus now so typing straight away lands in the box; once new text has
  // rendered, put the caret after it so the engineer can keep writing.
  const focusText = () => {
    const caretToEnd = () => {
      const box = textRef.current;
      box?.setSelectionRange(box.value.length, box.value.length);
    };
    textRef.current?.focus();
    caretToEnd();
    requestAnimationFrame(caretToEnd);
  };

  // A pin and chosen namespaces name things in one cluster. After a cluster
  // switch they would name same-named things somewhere else, so they go; the
  // words stay.
  const { data: contexts } = useContexts();
  const currentContext = contexts?.find((context) => context.isCurrent)?.name;
  const [chosenIn, setChosenIn] = useState(questionDraft?.context);
  const [droppedForCluster, setDroppedForCluster] = useState(false);

  // Where to start follows the namespace picker until the engineer edits it.
  const { data: scope } = useNamespaceScope();
  const [namespaces, setNamespaces] = useState<string[] | null>(
    questionDraft?.namespaces ?? null,
  );
  const picked = namespaces ?? scope?.actives ?? [];
  // Past a handful of namespaces, the whole cluster is the honest place to
  // start, and the backend accepts at most MAX_START_NAMESPACES.
  const tooMany = picked.length > MAX_START_NAMESPACES;
  const startIn = tooMany ? [] : picked;

  // Suggestions follow what the question is about: the pinned resource's
  // own issues, or the issues where the namespaces say to start.
  const { data: issuesData } = useIssues(startIn, !pin);
  const { data: pinIssues } = useResourceIssues(
    pin ? kindToPluralWithGroup(pin.kind, pin.group) : "",
    pin?.group || undefined,
    pin?.namespace ?? "",
    pin?.name ?? "",
    !!pin,
  );
  const asked = text.trim();
  const suggestions = useMemo(
    () =>
      (pin
        ? suggestionsForPin(pinIssues ?? [], pin)
        : suggestionsFromIssues(issuesData?.issues ?? [])
      ).filter((suggestion) => !asked.includes(suggestion.question)),
    [pin, pinIssues, issuesData, asked],
  );

  useEffect(() => {
    if (pickerOpen) pickerInputRef.current?.focus();
  }, [pickerOpen]);

  useEffect(() => {
    if (!currentContext) return;
    if (chosenIn && chosenIn !== currentContext && (pin || namespaces)) {
      setPin(null);
      setNamespaces(null);
      setDroppedForCluster(true);
    }
    if (chosenIn !== currentContext) setChosenIn(currentContext);
  }, [currentContext, chosenIn, pin, namespaces]);

  const unsent = useRef({
    text,
    pin,
    namespaces,
    lookBack,
    inherited,
    chosenIn,
  });
  unsent.current = { text, pin, namespaces, lookBack, inherited, chosenIn };
  // A submitted question belongs to its start, not the draft; the start gives
  // it back if it never becomes a run.
  const submitted = useRef(false);
  useEffect(
    () => () => {
      if (submitted.current) {
        saveQuestionDraft({});
        return;
      }
      const draft = unsent.current;
      saveQuestionDraft({
        askedAt:
          draft.inherited?.text === draft.text.trim()
            ? draft.inherited.askedAt
            : undefined,
        text: draft.text,
        pin: draft.pin ?? undefined,
        namespaces: draft.namespaces ?? undefined,
        lookBack: draft.lookBack,
        context: draft.chosenIn,
      });
    },
    [saveQuestionDraft],
  );

  // A new hand-off (Ask about a resource, a start that never became a run)
  // replaces what is here; the draft this composer started from does not.
  const appliedSeq = useRef(questionDraft?.seq);
  useEffect(() => {
    if (!questionDraft || questionDraft.seq === appliedSeq.current) return;
    appliedSeq.current = questionDraft.seq;
    if (questionDraft.restore && unsent.current.text.trim()) return;
    submitted.current = false;
    if (!questionDraft.keepWords) {
      setText(questionDraft.text ?? "");
      setInherited(
        questionDraft.askedAt && questionDraft.text
          ? { text: questionDraft.text.trim(), askedAt: questionDraft.askedAt }
          : null,
      );
    }
    setPin(questionDraft.pin ?? null);
    setNamespaces(questionDraft.namespaces ?? null);
    setChosenIn(questionDraft.context ?? currentContext);
    setDroppedForCluster(false);
    setLookBack(questionDraft.lookBack ?? DEFAULT_LOOK_BACK);
    setPickerOpen(!!questionDraft.pickResource);
    if (!questionDraft.pickResource) focusText();
  }, [questionDraft, currentContext]);

  // A hand-off that mounts this composer (the Omnibar row, Ask about a
  // resource, Back to your question) lands in the box, ready for Enter.
  useEffect(() => {
    if (questionDraft?.seq && !questionDraft.pickResource) focusText();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- mount only
  }, []);

  const question = text.trim();
  const tooLong = [...question].length > MAX_QUESTION_LENGTH;
  const submit = () => {
    if (!question || tooLong) return;
    submitted.current = true;
    openInvestigation({
      fromComposer: true,
      kind: pin?.kind ?? "",
      group: pin?.group ?? "",
      namespace: pin?.namespace ?? "",
      name: pin?.name ?? "",
      question,
      namespaces: pin ? [] : startIn,
      lookBack,
      askedAt: inherited?.text === question ? inherited.askedAt : undefined,
    });
  };

  // What the engineer already typed is their question; a suggestion then only
  // says where to start.
  const applySuggestion = (suggestion: QuestionSuggestion) => {
    if (!text.trim()) setText(suggestion.question);
    setPin(suggestion.pin ?? null);
    setNamespaces(suggestion.namespaces);
    focusText();
  };

  const addable =
    startIn.length >= MAX_START_NAMESPACES
      ? []
      : (scope?.accessibleNamespaces ?? []).filter(
          (ns) => !startIn.includes(ns),
        );

  return (
    <section className="mx-auto w-full max-w-2xl">
      <h1 className="text-xl font-semibold text-theme-text-primary">
        What are you seeing?
      </h1>
      <p className="mt-1 text-sm text-theme-text-tertiary">
        Describe a problem, or ask whether something looks right. {agentLabel}{" "}
        investigates it with Radar’s read-only tools and shows what it found,
        with the evidence.
      </p>

      <form
        className="mt-4 rounded-lg border border-theme-border bg-theme-surface shadow-theme-sm focus-within:border-accent"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <textarea
          ref={textRef}
          value={text}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            // "@" starting a word opens the picker; one inside a word (an
            // email, an image digest) is just text.
            if (event.key === "@" && !pin && !pickerOpen) {
              const box = event.currentTarget;
              const before = box.value.slice(0, box.selectionStart ?? 0);
              if (before === "" || /\s$/.test(before)) {
                event.preventDefault();
                setPickerOpen(true);
                return;
              }
            }
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing
            ) {
              event.preventDefault();
              submit();
            }
          }}
          rows={3}
          maxLength={MAX_QUESTION_LENGTH}
          aria-label="What are you seeing?"
          placeholder={
            "e.g. checkout returns 502s since the last deploy\ne.g. is catalog's memory use normal?"
          }
          className="block w-full resize-none rounded-t-lg bg-transparent px-3 py-2.5 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none"
        />
        <div className="flex flex-wrap items-center gap-1.5 border-t border-theme-border-subtle px-3 py-2">
          {pin ? (
            <span className={chipClass}>
              <span className="text-theme-text-tertiary">About</span>
              <span className="truncate font-medium text-theme-text-primary">
                {formatInvestigationTarget(pin)}
              </span>
              <button
                type="button"
                onClick={() => setPin(null)}
                aria-label="Unpin resource"
                className="rounded text-theme-text-tertiary hover:text-theme-text-primary"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ) : (
            <>
              <span className="text-xs text-theme-text-tertiary">Start in</span>
              {startIn.length === 0 ? (
                <Tooltip
                  disabled={!tooMany}
                  content={`${picked.length} namespaces are selected; investigations start across the cluster past ${MAX_START_NAMESPACES}.`}
                >
                  <span className={chipClass}>
                    All namespaces
                    {tooMany ? ` (${picked.length} selected)` : ""}
                  </span>
                </Tooltip>
              ) : (
                startIn.map((ns) => (
                  <span key={ns} className={chipClass}>
                    <span className="truncate">{ns}</span>
                    <button
                      type="button"
                      onClick={() =>
                        setNamespaces(startIn.filter((n) => n !== ns))
                      }
                      aria-label={`Remove namespace ${ns}`}
                      className="rounded text-theme-text-tertiary hover:text-theme-text-primary"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                ))
              )}
              {addable.length > 0 && (
                <select
                  value=""
                  onChange={(event) =>
                    event.target.value &&
                    setNamespaces([...startIn, event.target.value])
                  }
                  aria-label="Add a namespace to start in"
                  className="w-28 cursor-pointer rounded-md border border-dashed border-theme-border bg-transparent px-1.5 py-0.5 text-xs text-theme-text-tertiary hover:border-theme-text-tertiary hover:text-theme-text-primary"
                >
                  <option value="">+ namespace</option>
                  {addable.map((ns) => (
                    <option key={ns} value={ns}>
                      {ns}
                    </option>
                  ))}
                </select>
              )}
              <button
                type="button"
                onClick={() => setPickerOpen((open) => !open)}
                aria-expanded={pickerOpen}
                className={actionClass}
              >
                <AtSign className="h-3 w-3" />
                Pin a resource
              </button>
            </>
          )}
          <span className="ml-auto flex items-center gap-1">
            <span className="text-xs text-theme-text-tertiary">
              Started within
            </span>
            <span
              role="group"
              aria-label="Started within"
              className="inline-flex rounded-md border border-theme-border"
            >
              {LOOK_BACKS.map((value) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={lookBack === value}
                  onClick={() => setLookBack(value)}
                  className={`px-1.5 py-0.5 text-xs first:rounded-l-md last:rounded-r-md ${
                    lookBack === value
                      ? "bg-accent-muted text-accent-text"
                      : "text-theme-text-tertiary hover:bg-theme-hover"
                  }`}
                >
                  {value}
                </button>
              ))}
            </span>
            <button
              type="submit"
              disabled={!question || tooLong}
              className="btn-brand ml-1 inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium disabled:opacity-50"
            >
              Investigate
              <ArrowRight className="h-3.5 w-3.5" />
            </button>
          </span>
        </div>
        {tooLong && (
          <p className="border-t border-theme-border-subtle px-3 py-1.5 text-[11px] text-theme-text-tertiary">
            Questions can be up to {MAX_QUESTION_LENGTH} characters; this one
            has {[...question].length}.
          </p>
        )}
        {droppedForCluster && !pin && namespaces === null && (
          <p className="border-t border-theme-border-subtle px-3 py-1.5 text-[11px] text-theme-text-tertiary">
            The cluster changed, so the pinned resource and chosen namespaces
            were cleared. Your words are kept.
          </p>
        )}
        <Collapse open={pickerOpen && !pin} unmountOnExit>
          <ResourcePicker
            inputRef={pickerInputRef}
            onPick={(picked) => {
              setPin(picked);
              setPickerOpen(false);
              textRef.current?.focus();
            }}
            onCancel={() => {
              setPickerOpen(false);
              textRef.current?.focus();
            }}
          />
        </Collapse>
      </form>

      {suggestions.length > 0 && (
        <div className="mt-5">
          <h2 className="text-xs font-medium text-theme-text-tertiary">
            From what Radar sees now
          </h2>
          <ul className="mt-1.5 divide-y divide-theme-border-subtle overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
            {suggestions.map((suggestion) => {
              const SeverityIcon =
                suggestion.severity === "critical"
                  ? AlertOctagon
                  : AlertTriangle;
              return (
                <li key={suggestion.key}>
                  <button
                    type="button"
                    onClick={() => applySuggestion(suggestion)}
                    aria-label={`${ISSUE_SEVERITY_LABEL[suggestion.severity]}: ${suggestion.title}, ${suggestion.where}. ${text.trim() ? "Pins it to your question." : `Puts “${suggestion.question}” in the box.`}`}
                    className="group flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm hover:bg-theme-hover focus-visible:bg-theme-hover focus-visible:outline-none"
                  >
                    <SeverityIcon
                      aria-hidden
                      className={`h-4 w-4 shrink-0 ${ISSUE_SEVERITY_TEXT_CLASS[suggestion.severity]}`}
                    />
                    <span className="min-w-0 flex-1 truncate">
                      <span className="font-medium text-theme-text-primary">
                        {suggestion.title}
                      </span>
                      <span className="ml-2 text-theme-text-tertiary">
                        {suggestion.where}
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-1 text-xs text-theme-text-tertiary group-hover:text-theme-text-secondary group-focus-visible:text-theme-text-secondary">
                      <span className="opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100">
                        Use
                      </span>
                      <CornerDownLeft aria-hidden className="h-3.5 w-3.5" />
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {onBrowseIssues && (
        <button
          type="button"
          onClick={onBrowseIssues}
          className="mt-4 inline-flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
        >
          Or pick a problem from Issues
          <ArrowRight className="h-3 w-3" />
        </button>
      )}
    </section>
  );
}
