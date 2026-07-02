import { useCallback, useMemo, useRef, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { FilterLocationProvider, type FilterLocation } from '@skyhook-io/k8s-ui';

// Adapts OSS Radar's react-router search params to the app-agnostic
// FilterLocation seam that @skyhook-io/k8s-ui's useFilterState reads. Mounted
// once inside the router; every list view's shared filter state flows through
// it, keeping the URL the single source of truth. (Radar Hub provides its own
// bridge over its router — k8s-ui itself never depends on react-router.)
export function FilterLocationBridge({ children }: { children: ReactNode }) {
  const [searchParams, setSearchParams] = useSearchParams();

  // React Router's functional updater is NOT state-queued: two updates in one
  // tick can both read the same params and clobber. Advance a ref synchronously
  // so successive filter changes (e.g. toggling two facets fast) compose.
  const latest = useRef(searchParams);
  latest.current = searchParams;

  const update = useCallback<FilterLocation['update']>(
    (updater, opts) => {
      const next = updater(new URLSearchParams(latest.current));
      latest.current = next;
      setSearchParams(next, opts);
    },
    [setSearchParams],
  );

  const value = useMemo<FilterLocation>(() => ({ searchParams, update }), [searchParams, update]);
  return <FilterLocationProvider value={value}>{children}</FilterLocationProvider>;
}
