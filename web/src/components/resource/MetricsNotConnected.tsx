import { clsx } from "clsx";
import { Loader2, Wifi, WifiOff } from "lucide-react";
import {
  usePrometheusConnect,
  useDiscoverMetrics,
  type PrometheusStatus,
} from "../../api/client";
import { useNavCustomization } from "../../context/NavCustomization";
import { previousSettingsNote, usePreviousIntegrationSettings } from '../../hooks/usePreviousIntegrationSettings';

const openMetricsSettings = () => window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'prometheus' } }));

interface MetricsNotConnectedProps {
  status?: PrometheusStatus;
  isWorkload: boolean;
}

// A discovery result describes the status it ran against, so it starts over
// once the connection or its source changes.
export function MetricsNotConnected(props: MetricsNotConnectedProps) {
  const { status } = props;
  return <NotConnected key={`${status?.connected}|${status?.source?.kind}|${status?.source?.via}`} {...props} />;
}

function NotConnected({ status, isWorkload }: MetricsNotConnectedProps) {
  const connectMutation = usePrometheusConnect();
  const discoverMutation = useDiscoverMetrics();
  const settingsAvailable = !useNavCustomization().embedded;
  const offers = usePreviousIntegrationSettings(!!status);
  const previousNote = offers.metrics ? previousSettingsNote('metrics', offers.metrics) : undefined;
  const openObservePath = status?.openobserve;
  const discovery = openObservePath ? discoverMutation : connectMutation;
  const outcome = openObservePath ? discoverMutation.data : undefined;
  const failure = openObservePath ? discoverMutation.error?.message : undefined;
  const notFound = outcome === "none" || outcome === "collector";
  return (
    <div className="flex flex-col items-center justify-center py-12 gap-4">
      <WifiOff className="w-10 h-10 text-theme-text-quaternary" />
      <div className="text-center">
        <p className="text-sm text-theme-text-secondary mb-1">
          {openObservePath ? "No metrics source connected" : "Prometheus not connected"}
        </p>
        <p className="text-xs text-theme-text-tertiary mb-4">
          {status?.error || (openObservePath
            ? "Radar can read metrics from Prometheus or OpenObserve."
            : "Connect to view historical CPU, memory, and network metrics")}
        </p>
        {(notFound || failure) && <p role="status" className="text-sm text-theme-text-secondary mb-4">
          {failure
            ? failure
            : outcome === "none"
              ? "No metrics source found."
              : openObservePath === "hub"
                ? "OpenObserve collector found. Ask your organization owner to connect OpenObserve in Radar Hub."
                : "OpenObserve collector found. Connect it in Settings → Metrics."}
        </p>}
        <div className="flex flex-wrap items-center justify-center gap-3">
        {!notFound && <button
          onClick={() => discovery.mutate()}
          disabled={discovery.isPending}
          className="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium rounded-lg btn-brand"
        >
          {discovery.isPending ? (
            <Loader2 className="w-4 h-4 animate-spin" />
          ) : (
            <Wifi className="w-4 h-4" />
          )}
          {openObservePath ? "Discover metrics" : "Discover Prometheus"}
        </button>}
        {settingsAvailable && <button
          type="button"
          className="text-sm text-accent hover:underline"
          onClick={openMetricsSettings}
        >Configure metrics</button>}
        </div>
        {previousNote && <p className="mt-3 text-xs text-theme-text-secondary">{previousNote}</p>}
        {!settingsAvailable && <p className="mt-3 text-xs text-theme-text-tertiary">Ask your operator to configure the metrics connection for this cluster.</p>}
        {isWorkload && <a className="mt-3 inline-block text-xs text-accent hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/workload-metrics.md#what-each-chart-needs" target="_blank" rel="noopener noreferrer">What each chart needs</a>}
      </div>
    </div>
  );
}

export function OpenObserveUnavailable({
  status,
  compact = false,
}: {
  status: PrometheusStatus;
  compact?: boolean;
}) {
  const viaHub = status.source?.via === "hub";
  const error = status.source?.error;
  const settingsAvailable = !useNavCustomization().embedded;
  return (
    <div className={clsx("flex flex-col items-center justify-center gap-4", compact ? "py-4" : "py-12")}>
      <WifiOff className="w-10 h-10 text-theme-text-quaternary" />
      <div className="text-center">
        <p className="text-sm text-theme-text-secondary mb-1">OpenObserve unavailable</p>
        {error && <p className="text-xs text-theme-text-tertiary mb-1">{error}</p>}
        <p className="text-xs text-theme-text-tertiary">
          {viaHub
            ? "This cluster's metrics come from OpenObserve through Radar Hub. Charts come back when it answers again."
            : "This cluster's metrics come from the saved OpenObserve connection. Charts come back when it answers again."}
        </p>
        {!viaHub && settingsAvailable && <button
          type="button"
          className="mt-3 text-sm text-accent hover:underline"
          onClick={openMetricsSettings}
        >Configure metrics</button>}
      </div>
    </div>
  );
}
