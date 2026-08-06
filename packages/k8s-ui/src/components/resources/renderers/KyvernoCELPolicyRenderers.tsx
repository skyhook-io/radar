// Renderers for Kyverno's modern CEL policy family (policies.kyverno.io),
// stabilized in Kyverno 1.17/1.18 and the family that survives the removal of
// kyverno.io Policy/ClusterPolicy planned for 1.20.
//
// One renderer per behavioural family, each sharing the posture header and
// match-scope sections from KyvernoPolicyShared. The five specs overlap far
// less than their names suggest — DeletingPolicy has no failurePolicy,
// evaluation or webhookConfiguration at all and is scheduled rather than
// admission-driven — so a merged renderer would be mostly branches.
//
// Each Namespaced* twin renders through its cluster-scoped counterpart: same
// spec, narrower scope, and the scope is already conveyed by the namespace in
// the resource header.

import { FileWarning, ShieldCheck, Wand2, Sparkles, Trash2, Clock, Fingerprint, ScrollText } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  KyvernoEvaluationSection,
  KyvernoExpressionList,
  KyvernoMatchScopeSection,
  KyvernoPostureHeader,
  KyvernoVariablesSection,
} from './KyvernoPolicyShared'
import {
  getKyvernoAttestations,
  getKyvernoAttestors,
  getKyvernoDeletionConditions,
  getKyvernoDeletionPropagationPolicy,
  getKyvernoGenerationSettings,
  getKyvernoGenerations,
  getKyvernoImageExtractors,
  getKyvernoImageReferences,
  getKyvernoMutations,
  getKyvernoPolicyConditions,
  getKyvernoReinvocationPolicy,
  getKyvernoSchedule,
  getKyvernoValidations,
  type KyvernoCELValidation,
} from '../resource-utils-kyverno-modern'

/**
 * The CEL rules themselves, each with the message an operator sees when it
 * fails. Expression and message belong together — a rule without its message
 * is unreadable, and a message without its rule is unverifiable.
 */
function ValidationsSection({ validations }: { validations: KyvernoCELValidation[] }) {
  if (validations.length === 0) return null
  return (
    <Section title={`Validations (${validations.length})`} icon={FileWarning}>
      <div className="space-y-2">
        {validations.map((v, i) => (
          <div key={i} className="card-inner-lg">
            <pre className="text-xs text-theme-text-primary font-mono whitespace-pre-wrap break-all">
              {v.expression}
            </pre>
            {(v.message || v.messageExpression) && (
              <div className="mt-2 text-xs text-theme-text-secondary">
                {v.message || <span className="font-mono">{v.messageExpression}</span>}
              </div>
            )}
            {v.reason && (
              <div className="mt-1">
                <span className="badge status-neutral">{v.reason}</span>
              </div>
            )}
          </div>
        ))}
      </div>
    </Section>
  )
}

export function KyvernoValidatingPolicyRenderer({ data }: { data: any }) {
  return (
    <div className="space-y-4">
      <KyvernoPostureHeader data={data} family="validating" />
      <ValidationsSection validations={getKyvernoValidations(data)} />
      <KyvernoMatchScopeSection data={data} />
      <KyvernoVariablesSection data={data} />
      <KyvernoEvaluationSection data={data} />
      <ConditionsSection conditions={getKyvernoPolicyConditions(data)} />
    </div>
  )
}

export function KyvernoImageValidatingPolicyRenderer({ data }: { data: any }) {
  const attestors = getKyvernoAttestors(data)
  const attestations = getKyvernoAttestations(data)
  const imageRefs = getKyvernoImageReferences(data)
  const extractors = getKyvernoImageExtractors(data)

  return (
    <div className="space-y-4">
      <KyvernoPostureHeader data={data} family="imageValidating" />

      {imageRefs.length > 0 && (
        <Section title={`Image References (${imageRefs.length})`} icon={ScrollText}>
          <div className="space-y-1">
            {imageRefs.map((ref, i) => (
              <div key={i} className="text-xs font-mono text-theme-text-primary break-all">
                {ref}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Attestors are the trust anchor — who must have signed. Without one,
          image verification has nothing to check against. */}
      {attestors.length > 0 ? (
        <Section title={`Attestors (${attestors.length})`} icon={Fingerprint}>
          <div className="space-y-2">
            {attestors.map((a, i) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 flex-wrap">
                  {a.name && <span className="text-xs font-medium text-theme-text-primary">{a.name}</span>}
                  {a.kind && <span className="badge status-neutral">{a.kind}</span>}
                </div>
                {a.identity && (
                  <div className="mt-1 text-xs font-mono text-theme-text-secondary break-all">{a.identity}</div>
                )}
              </div>
            ))}
          </div>
        </Section>
      ) : (
        <AlertBanner
          variant="warning"
          title="No attestors configured"
          message="This image policy has no trusted authority to verify signatures against."
        />
      )}

      {attestations.length > 0 && (
        <Section title={`Attestations (${attestations.length})`} icon={ShieldCheck}>
          <div className="space-y-2">
            {attestations.map((a, i) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 flex-wrap">
                  {a.name && <span className="text-xs font-medium text-theme-text-primary">{a.name}</span>}
                  {a.kind && <span className="badge status-neutral">{a.kind}</span>}
                </div>
                {a.type && <div className="mt-1 text-xs font-mono text-theme-text-secondary break-all">{a.type}</div>}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ValidationsSection validations={getKyvernoValidations(data)} />

      {extractors.length > 0 && (
        <Section title={`Image Extractors (${extractors.length})`} icon={ScrollText} defaultExpanded={false}>
          <KyvernoExpressionList items={extractors} />
        </Section>
      )}

      <KyvernoMatchScopeSection data={data} />
      <KyvernoVariablesSection data={data} />
      <KyvernoEvaluationSection data={data} />
      <ConditionsSection conditions={getKyvernoPolicyConditions(data)} />
    </div>
  )
}

export function KyvernoMutatingPolicyRenderer({ data }: { data: any }) {
  const mutations = getKyvernoMutations(data)
  const reinvocation = getKyvernoReinvocationPolicy(data)

  return (
    <div className="space-y-4">
      <KyvernoPostureHeader data={data} family="mutating" />

      {mutations.length > 0 && (
        <Section title={`Mutations (${mutations.length})`} icon={Wand2}>
          <div className="space-y-2">
            {mutations.map((m, i) => (
              <div key={i} className="card-inner-lg">
                {m.patchType && (
                  <div className="mb-1">
                    <span className="badge status-neutral">{m.patchType}</span>
                  </div>
                )}
                <pre className="text-xs text-theme-text-primary font-mono whitespace-pre-wrap break-all">
                  {m.expression}
                </pre>
              </div>
            ))}
          </div>
        </Section>
      )}

      {reinvocation && (
        <Section title="Reinvocation" icon={Wand2} defaultExpanded={false}>
          <PropertyList>
            <Property
              label="Policy"
              value={
                <span className="flex items-center gap-2">
                  <span className="badge status-neutral">{reinvocation}</span>
                  <span className="text-xs text-theme-text-tertiary">
                    {reinvocation === 'IfNeeded'
                      ? 're-runs when another webhook changes the object'
                      : 'runs once per admission request'}
                  </span>
                </span>
              }
            />
          </PropertyList>
        </Section>
      )}

      <KyvernoMatchScopeSection data={data} />
      <KyvernoVariablesSection data={data} />
      <KyvernoEvaluationSection data={data} />
      <ConditionsSection conditions={getKyvernoPolicyConditions(data)} />
    </div>
  )
}

export function KyvernoGeneratingPolicyRenderer({ data }: { data: any }) {
  const generations = getKyvernoGenerations(data)
  const settings = getKyvernoGenerationSettings(data)

  return (
    <div className="space-y-4">
      <KyvernoPostureHeader data={data} family="generating" />

      {generations.length > 0 && (
        <Section title={`Generates (${generations.length})`} icon={Sparkles}>
          <div className="space-y-2">
            {generations.map((expr, i) => (
              <div key={i} className="card-inner-lg">
                <pre className="text-xs text-theme-text-primary font-mono whitespace-pre-wrap break-all">{expr}</pre>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* These three decide what happens to already-generated objects, which
          is where generation surprises operators: whether existing resources
          are backfilled, whether edits are reverted, and whether downstream
          objects survive deleting the policy. */}
      <Section title="Generation Behavior" icon={Sparkles}>
        <PropertyList>
          <Property
            label="Generate Existing"
            value={settings.generateExisting ? 'Yes — applies to resources that already exist' : 'No — new resources only'}
          />
          <Property
            label="Synchronize"
            value={
              settings.synchronize
                ? 'Yes — manual edits to generated resources are reverted'
                : 'No — generated resources are left alone after creation'
            }
          />
          <Property
            label="Orphan On Delete"
            value={
              settings.orphanDownstreamOnPolicyDelete
                ? 'Yes — generated resources survive deleting this policy'
                : 'No — generated resources are removed with this policy'
            }
          />
        </PropertyList>
      </Section>

      <KyvernoMatchScopeSection data={data} />
      <KyvernoVariablesSection data={data} />
      <ConditionsSection conditions={getKyvernoPolicyConditions(data)} />
    </div>
  )
}

export function KyvernoDeletingPolicyRenderer({ data }: { data: any }) {
  const schedule = getKyvernoSchedule(data)
  const conditions = getKyvernoDeletionConditions(data)
  const propagation = getKyvernoDeletionPropagationPolicy(data)

  return (
    <div className="space-y-4">
      <KyvernoPostureHeader data={data} family="deleting" />

      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property
            label="Cron"
            value={schedule ? <span className="font-mono">{schedule}</span> : 'Not set — this policy will not run'}
          />
          {propagation && <Property label="Deletion Propagation" value={propagation} />}
        </PropertyList>
      </Section>

      {/* Without conditions every matched resource is deleted on schedule.
          That is a materially different blast radius and has to be stated
          rather than left as an empty section. */}
      {conditions.length > 0 ? (
        <Section title={`Deletion Conditions (${conditions.length})`} icon={Trash2}>
          <KyvernoExpressionList items={conditions} />
        </Section>
      ) : (
        <AlertBanner
          variant="warning"
          icon={Trash2}
          title="No deletion conditions"
          message="Every resource matched by this policy is deleted when the schedule fires."
        />
      )}

      <KyvernoMatchScopeSection data={data} />
      <KyvernoVariablesSection data={data} />
      <ConditionsSection conditions={getKyvernoPolicyConditions(data)} />
    </div>
  )
}
