import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { AgentSigninNotice } from "./AgentSigninNotice";
import { ConsentCard } from "./parts";

const noop = vi.fn();

describe("AgentSigninNotice", () => {
  it("names the agent, the command to run and a re-check", () => {
    const html = renderToStaticMarkup(
      <AgentSigninNotice
        agentName="Claude Code"
        signinCmd="claude auth login"
        rechecking={false}
        onRecheck={noop}
      />,
    );
    expect(html).toContain("Claude Code isn&#x27;t signed in");
    expect(html).toContain("claude auth login");
    expect(html).toContain("Copy sign-in command");
    expect(html).toContain("Check again");
    expect(html).not.toContain("Still not signed in");
  });

  it("shows the re-check in progress", () => {
    const html = renderToStaticMarkup(
      <AgentSigninNotice
        agentName="Codex"
        signinCmd="codex login"
        rechecking
        onRecheck={noop}
      />,
    );
    expect(html).toContain("Checking…");
    expect(html).toContain("disabled");
  });
});

describe("ConsentCard sign-in notice", () => {
  it("shows the notice above the actions and keeps Approve available", () => {
    const html = renderToStaticMarkup(
      <ConsentCard
        agentName="Claude Code"
        agent="claude"
        profile="safeguarded"
        notice={
          <AgentSigninNotice
            agentName="Claude Code"
            signinCmd="claude auth login"
            rechecking={false}
            onRecheck={noop}
          />
        }
        onApprove={noop}
        onCancel={noop}
      />,
    );
    const notice = html.indexOf("isn&#x27;t signed in");
    const approve = html.indexOf("Approve &amp; investigate");
    expect(notice).toBeGreaterThan(-1);
    expect(approve).toBeGreaterThan(notice);
    const approveButton = html.slice(html.lastIndexOf("<button", approve), approve);
    expect(approveButton).not.toContain("disabled");
  });

  it("says Approve is waiting on the check", () => {
    const html = renderToStaticMarkup(
      <ConsentCard
        agentName="Codex"
        agent="codex"
        profile="safeguarded"
        approveBusy
        onApprove={noop}
        onCancel={noop}
      />,
    );
    const label = html.indexOf("Checking sign-in…");
    expect(label).toBeGreaterThan(-1);
    expect(html.slice(html.lastIndexOf("<button", label), label)).toContain(
      "disabled",
    );
    expect(html).not.toContain("Approve &amp; investigate");
  });

  it("renders as before without a notice (signed in or unknown)", () => {
    const html = renderToStaticMarkup(
      <ConsentCard
        agentName="Claude Code"
        agent="claude"
        profile="safeguarded"
        onApprove={noop}
        onCancel={noop}
      />,
    );
    expect(html).not.toContain("signed in");
    expect(html).toContain("Approve &amp; investigate");
  });
});
