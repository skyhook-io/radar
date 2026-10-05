import { useContextSwitchFlow } from "../useContextSwitchFlow";
import { useState } from "react";
import { FolderOpen, Copy } from "lucide-react";
import { useOpenDatumProject } from "../../api/datum";
import { useConnection } from "../../context/ConnectionContext";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
export function DatumProjectConnection({ project }: { project: any }) {
  const { connection } = useConnection();
  const navigate = useWorkspaceNavigate();
  const mutation = useOpenDatumProject(() => navigate("/datum"));
  const { requestSwitch, confirmDialog } = useContextSwitchFlow();
  const [copied, setCopied] = useState(false);
  const command = `datumctl auth update-kubeconfig --project ${project.metadata.name}`;
  return (
    <div className="space-y-2" onClick={(e) => e.stopPropagation()}>
      {confirmDialog}
      <button
        className="btn-brand inline-flex gap-1.5 items-center px-3 py-1.5 text-xs"
        disabled={mutation.isPending || !!project.metadata.deletionTimestamp}
        onClick={() =>
          requestSwitch(
            {
              name: project.metadata.name,
              cluster: project.metadata.name,
              user: "",
              namespace: "",
              isCurrent: false,
            },
            undefined,
            () =>
              mutation.mutateAsync({
                name: project.metadata.name,
                uid: project.metadata.uid,
                reviewedContext: connection.context || "",
              }),
          )
        }
      >
        <FolderOpen className="h-3.5 w-3.5" />
        {mutation.isPending
          ? "Verifying and connecting…"
          : "Open project control plane"}
      </button>
      <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
        <code className="break-all">{command}</code>
        <button
          type="button"
          aria-label="Copy datumctl command"
          onClick={async () => {
            await navigator.clipboard.writeText(command);
            setCopied(true);
          }}
          className="text-theme-text-secondary hover:text-theme-text-primary"
        >
          <Copy className="h-3.5 w-3.5" />
        </button>
        {copied && <span>Copied</span>}
      </div>
      <p className="text-[11px] text-theme-text-tertiary">
        Radar reuses the parent connection in memory. The command updates your
        kubeconfig when you run it.
      </p>
    </div>
  );
}
