import type { SearchMatchedField } from '../../api/client'

// contentMatchLabel names where a hit matched when every token landed in the
// object's content rather than its name, kind or namespace. Trivy Operator
// reports keep their findings under `report`, not a spec.
export function contentMatchLabel(matched: SearchMatchedField[] | undefined): string {
  if (!matched?.length || !matched.every((m) => m.site.startsWith('content:'))) return ''
  return matched.every((m) => m.site.startsWith('content:report.')) ? 'in findings' : 'in spec'
}

// partialSearchNotice explains a result the server says is incomplete, so a
// missing CVE doesn't read as "not affected". Null when the search was whole.
export function partialSearchNotice(data: { incomplete_findings?: string; access_incomplete?: boolean } | undefined): string | null {
  if (data?.incomplete_findings || data?.access_incomplete) {
    return 'Some scan reports were not fully searched. Try again, or narrow with ns:.'
  }
  return null
}
