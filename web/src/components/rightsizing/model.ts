import type { RightsizingScanResponse } from '../../api/client'
import {
  compareScanResults,
  workloadResultRows,
  type RightsizingResultRow,
  isSizingSystemNamespace,
} from '@skyhook-io/k8s-ui/components/rightsizing/results-model'

export type { ScanClass } from '@skyhook-io/k8s-ui/components/rightsizing/results-model'

export interface RightsizingScanRow extends RightsizingResultRow {
  system: boolean
}

export function flattenScanResults(scan: RightsizingScanResponse): RightsizingScanRow[] {
  return scan.workloads
    .flatMap((workload) =>
      workloadResultRows(workload).map((row) => ({
        ...row,
        system: isSizingSystemNamespace(row.namespace),
      })),
    )
    .sort(compareScanResults)
}
