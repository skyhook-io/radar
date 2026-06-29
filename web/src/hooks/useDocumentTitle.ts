import { useEffect } from 'react';

// Restore-on-unmount matters for overlay/detail views: closing a resource drawer
// that opened over a list returns the list's title rather than stranding the
// resource's. document.title is global to the page, so embedders that don't own
// the whole tab pass a falsy `label` to opt out and keep their own title
// (AppInner only feeds a label when the host passed `manageDocumentTitle`).
//
// `suffix` is the full trailing string after the label, so a host can rebrand
// (' — My Cloud') or drop it entirely ('').
const DEFAULT_SUFFIX = ' · Radar';

export function useDocumentTitle(
  label: string | null | undefined,
  suffix: string = DEFAULT_SUFFIX,
): void {
  useEffect(() => {
    if (!label) return;
    const previous = document.title;
    document.title = `${label}${suffix}`;
    return () => {
      document.title = previous;
    };
  }, [label, suffix]);
}
