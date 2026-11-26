// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, it, vi } from 'vitest';
import { IssuesView } from './IssuesView';
import type { Issue } from './types';

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
const app = { key: 'checkout', name: 'checkout' };
const issue = (name: string, severity: Issue['severity'], first_seen: string): Issue => ({
  id: name, name, severity, first_seen, namespace: 'store', kind: 'Deployment', grouping_scope: 'workload',
  reason: 'CrashLoopBackOff', source: 'problem', category: 'crashloop', category_group: 'runtime',
});
const issues = [
  issue('api', 'critical', '2026-09-01'), issue('unrelated', 'critical', '2026-08-01'),
  issue('front', 'critical', '2026-07-15'), issue('cache', 'critical', '2026-07-10'),
  issue('worker', 'critical', '2026-07-01'), issue('web', 'warning', '2026-06-01'),
  issue('batch', 'warning', '2026-05-01'),
];
const owner = (i: Issue) => i.id === 'unrelated' ? undefined : app;
const cleanup: (() => void)[] = [];
afterEach(() => cleanup.splice(0).forEach(fn => fn()));

async function mount() {
  const host = document.createElement('div');
  const root = createRoot(host);
  document.body.append(host);
  cleanup.push(() => { act(() => root.unmount()); host.remove(); });
  const seen = vi.fn();
  const detail = vi.fn();
  const render = async (linkedIssueId?: string, inventory = true, rows = issues) =>
    act(async () => root.render(<IssuesView anyData issues={rows} linkedIssueId={linkedIssueId}
      appForIssue={inventory ? owner : undefined} onVisibleIssuesChange={seen} onOpenIssueChange={detail} />));
  const displayed = () => [...host.querySelectorAll<HTMLElement>('[data-app-issue-row]')].map(el => el.dataset.appIssueRow);
  return { render, displayed, toggle: () => [...host.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.includes(' for checkout')), seen, detail, host };
}

it('keeps the first row in order, counts only hidden criticals, and enriches only displayed rows', async () => {
  const { render, displayed, toggle, seen } = await mount();
  await render();
  expect(displayed()).toEqual([':api', ':front', ':cache', ':unrelated']);
  expect(toggle()?.textContent).toContain('Show 3 more issues for checkout');
  expect(toggle()?.textContent).toContain('1 critical: Crash loop');
  expect(seen.mock.lastCall?.[0].map((i: Issue) => i.id)).toEqual(['api', 'front', 'cache', 'unrelated']);
  await act(async () => toggle()!.click());
  expect(displayed()).toEqual([':api', ':front', ':cache', ':worker', ':web', ':batch', ':unrelated']);
  await render(undefined, true, [issues[0], issues[1]]);
  expect(toggle()).toBeUndefined();
});

it('reveals the linked issue on arrival, after inventory loads, and on a new link within the same app', async () => {
  const { render, displayed, toggle, detail } = await mount();
  await render('worker', false);
  expect(detail.mock.lastCall?.[0]?.id).toBe('worker');
  await render('worker');
  expect(toggle()?.getAttribute('aria-expanded')).toBe('true');
  expect(displayed()).toContain(':worker');
  await act(async () => toggle()!.click());
  await render('worker', true, [...issues]);
  expect(toggle()?.getAttribute('aria-expanded')).toBe('false');
  expect(detail.mock.lastCall?.[0]).toBeNull();
  await render('web');
  expect(toggle()?.getAttribute('aria-expanded')).toBe('true');
  expect(detail.mock.lastCall?.[0]?.id).toBe('web');
});

it('keeps an issue open when late application inventory folds it beneath a higher-ranked row', async () => {
  const { render, toggle, host, detail } = await mount();
  await render(undefined, false);
  const worker = host.querySelector<HTMLElement>('[data-app-issue-row=":worker"] [aria-expanded="false"]');
  expect(worker).toBeTruthy();
  await act(async () => worker!.click());
  expect(detail.mock.lastCall?.[0]?.id).toBe('worker');
  await render();
  expect(toggle()?.getAttribute('aria-expanded')).toBe('true');
  expect(detail.mock.lastCall?.[0]?.id).toBe('worker');
});

it('preserves a manual collapse when the linked row leaves and reenters the results', async () => {
  const { render, toggle, detail } = await mount();
  await render('web');
  await act(async () => toggle()!.click());
  await render('web', true, issues.slice(0, 3));
  await render('web', true, [...issues]);
  expect(toggle()?.getAttribute('aria-expanded')).toBe('false');
  expect(detail.mock.lastCall?.[0]).toBeNull();
});

it('keeps keyboard focus on the disclosure when a new issue leads the group', async () => {
  const { render, toggle } = await mount();
  await render();
  toggle()!.focus();
  await render(undefined, true, issues.slice(1));
  expect(document.activeElement).toBe(toggle());
});

it('keeps the same issue summary focused while application membership and rank change', async () => {
  const { render, host } = await mount();
  await render(undefined, false);
  const summary = host.querySelector<HTMLButtonElement>('[data-app-issue-row=":api"] [aria-expanded]')!;
  await act(async () => summary.click());
  summary.focus();
  await render();
  expect(document.activeElement).toBe(summary);
  await render(undefined, true, [issue('newer', 'critical', '2026-10-01'), ...issues]);
  expect(document.activeElement).toBe(summary);
});

it('shows small groups in full and gives only related issues a panel heading', async () => {
  const { render, displayed, toggle, host } = await mount();
  for (const count of [1, 2, 3, 4, 5]) {
    const appIssues = issues.filter(i => owner(i)).slice(0, count);
    await render(undefined, true, [...appIssues, issues[1]]);
    expect(host.querySelector('h2')?.textContent).toBe(count > 1 ? 'checkout' : undefined);
    expect(toggle() !== undefined).toBe(count > 4);
    expect(displayed().length).toBe((count > 4 ? 3 : count) + 1);
  }
});

it('keeps overflow content mounted and inert until the closing animation finishes', async () => {
  const { render, toggle, displayed } = await mount();
  await render();
  const button = toggle()!;
  const firstOverflow = document.getElementById(button.getAttribute('aria-controls')!.split(' ')[0])!;
  await act(async () => button.click());
  const summary = firstOverflow.querySelector('[aria-expanded]')!;
  expect(summary).toBeTruthy();

  await act(async () => button.click());
  expect(displayed()).not.toContain(':worker');
  expect(summary.closest('[inert]')).toBeTruthy();
  const transition = new Event('transitionend', { bubbles: true });
  Object.defineProperty(transition, 'propertyName', { value: 'grid-template-rows' });
  await act(async () => { firstOverflow.firstElementChild!.dispatchEvent(transition); });
  expect(firstOverflow.contains(summary)).toBe(false);
});

it('opens a preview row independently and focuses the newly revealed issue on expansion', async () => {
  const { render, toggle, host } = await mount();
  await render();
  const summary = host.querySelector<HTMLButtonElement>('[data-app-issue-row=":front"] [aria-expanded]')!;
  await act(async () => summary.click());
  expect(toggle()?.getAttribute('aria-expanded')).toBe('false');
  await act(async () => toggle()!.click());
  expect(document.activeElement?.closest('[data-app-issue-row]')?.getAttribute('data-app-issue-row')).toBe(':worker');
  await act(async () => toggle()!.click());
  expect(document.activeElement).toBe(toggle());
  expect(summary.getAttribute('aria-expanded')).toBe('true');
});
