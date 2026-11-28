// Kargo 1.12.7 objects from a live kargo-demo project (test -> uat -> prod, one
// image Warehouse), trimmed to the fields Radar reads. State: test and uat hold
// nginx 1.31.10, prod holds 1.31.9; test's last promotion errored at git push;
// uat's verification of 1.31.10 failed. Not exported from the package.

import type { KargoFreight, KargoFreightContent, KargoPromotion, KargoStage, KargoWarehouse } from './resource-utils-kargo'

export const NOW = Date.parse('2026-11-25T13:10:00Z')

export const NEW_FREIGHT = 'b56f6e30e82989f403c760e5d534403854ce59ef'
export const OLD_FREIGHT = 'c9e2bddf7415b036e09b96946d8ecf3a9a2257e5'

const API = 'kargo.akuity.io/v1alpha1'
const NS = 'kargo-demo'
const origin = { kind: 'Warehouse', name: 'kargo-demo' }

const newRef: KargoFreightContent = {
  name: NEW_FREIGHT,
  origin,
  images: [{ repoURL: 'public.ecr.aws/nginx/nginx', tag: '1.31.10', digest: 'sha256:9ce62af8c757d8e49d85804dc72092119b1ff80a3133e78dd968842b9f9dda07' }],
}
const oldRef: KargoFreightContent = {
  name: OLD_FREIGHT,
  origin,
  images: [{ repoURL: 'public.ecr.aws/nginx/nginx', tag: '1.31.9', digest: 'sha256:7cdd7d67af427e78edfa77db29cc13a9ada62cc0c89beaf05f8d3c5d3f9ac3da' }],
}

const TEST_PUSH_ERROR = 'step "promo-process::step-6" met error threshold of 1: error running step "promo-process::step-6": error pushing commits to remote: error pushing branch: error executing cmd [/usr/bin/git push origin HEAD:stage/test]: fatal: could not read Username for \'http://gitea.gitea.svc.cluster.local:3000\': No such device or address\n'
const UAT_VERIFY_ERROR = 'Metric "error-rate" assessed Failed due to failed (1) > failureLimit (0)'

export const newFreight: KargoFreight = {
  apiVersion: API,
  kind: 'Freight',
  alias: 'illocutionary-skunk',
  discoveredAt: '2026-11-25T13:08:04Z',
  metadata: { name: NEW_FREIGHT, namespace: NS, labels: { 'kargo.akuity.io/alias': 'illocutionary-skunk' }, creationTimestamp: '2026-11-25T13:08:04Z' },
  origin,
  images: newRef.images,
  status: {
    currentlyIn: { test: { since: '2026-11-25T13:08:50Z' }, uat: { since: '2026-11-25T13:09:05Z' } },
    verifiedIn: { test: { verifiedAt: '2026-11-25T13:09:00Z' } },
  },
}

export const oldFreight: KargoFreight = {
  apiVersion: API,
  kind: 'Freight',
  alias: 'fallacious-robin',
  discoveredAt: '2026-11-25T13:08:06Z',
  metadata: { name: OLD_FREIGHT, namespace: NS, labels: { 'kargo.akuity.io/alias': 'fallacious-robin' }, creationTimestamp: '2026-11-25T13:08:07Z' },
  origin,
  images: oldRef.images,
  status: {
    currentlyIn: { prod: { since: '2026-11-25T13:08:45Z' } },
    verifiedIn: {
      prod: { verifiedAt: '2026-11-25T13:08:45Z' },
      test: { longestSoak: '35.469301832s', verifiedAt: '2026-11-25T13:08:28Z' },
      uat: { longestSoak: '36.561201046s', verifiedAt: '2026-11-25T13:08:39Z' },
    },
  },
}

export const warehouse: KargoWarehouse = {
  apiVersion: API,
  kind: 'Warehouse',
  metadata: { name: 'kargo-demo', namespace: NS },
  spec: {
    freightCreationPolicy: 'Automatic',
    interval: '5m0s',
    subscriptions: [{ image: { constraint: '^1.29.0', discoveryLimit: 5, imageSelectionStrategy: 'SemVer', repoURL: 'public.ecr.aws/nginx/nginx', strictSemvers: true } }],
  },
  status: {
    conditions: [
      { type: 'Ready', status: 'True', reason: 'ArtifactsDiscovered', message: 'Successfully discovered artifacts from 1 subscriptions' },
      { type: 'Healthy', status: 'True', reason: 'ReconciliationSucceeded', message: '' },
      { type: 'FreightCreationCriteriaSatisfied', status: 'True', reason: 'CriteriaSatisfied' },
      { type: 'FreightCreated', status: 'False', reason: 'AlreadyExists', message: 'Freight composed of the newest artifacts already exists' },
    ],
    discoveredArtifacts: { discoveredAt: '2026-11-25T13:08:04Z' },
    lastFreightID: NEW_FREIGHT,
  },
}

const promotionTemplate = { spec: { steps: [{ as: 'promo-process', task: { name: 'demo-promo-process' } }] } }

export const testStage: KargoStage = {
  apiVersion: API,
  kind: 'Stage',
  metadata: { name: 'test', namespace: NS, annotations: { 'kargo.akuity.io/argocd-context': '[{"name":"kargo-demo-test","namespace":"argocd"}]' } },
  spec: {
    promotionTemplate,
    requestedFreight: [{ origin, sources: { direct: true } }],
    verification: { analysisTemplates: [{ name: 'smoke' }] },
  },
  status: {
    conditions: [
      { type: 'Ready', status: 'False', reason: 'LastPromotionErrored', message: TEST_PUSH_ERROR },
      { type: 'Healthy', status: 'Unknown', reason: 'LastPromotionErrored', message: 'Cannot assess health because last Promotion did not succeed' },
      { type: 'Verified', status: 'True', reason: 'Verified', message: 'Freight has been verified' },
    ],
    freightHistory: [
      {
        id: '7c6082af37571cf271f35aca5d5245afca02d72f',
        items: { 'Warehouse/kargo-demo': newRef },
        verificationHistory: [{
          analysisRun: { name: 'test.01m89d5fqebp5h7ydjpnvm8vrn.7c6082a', namespace: NS, phase: 'Successful' },
          finishTime: '2026-11-25T13:09:00Z',
          id: '792b0508-d5e8-40b8-8c7a-492a2681d20b',
          phase: 'Successful',
          startTime: '2026-11-25T13:08:51Z',
        }],
      },
      { id: '7bbc8df2bedd6928df2f79b98f9e8cd449ac072a', items: { 'Warehouse/kargo-demo': oldRef } },
    ],
    health: { issues: ['Cannot assess health because last Promotion did not succeed'], status: 'Unknown' },
    lastPromotion: {
      finishedAt: '2026-11-25T13:09:27Z',
      freight: newRef,
      name: 'test.01m89d6j01phpbye0vx2ftjhrh.b56f6e3',
      status: { phase: 'Errored', message: TEST_PUSH_ERROR },
    },
  },
}

export const uatStage: KargoStage = {
  apiVersion: API,
  kind: 'Stage',
  metadata: { name: 'uat', namespace: NS, annotations: { 'kargo.akuity.io/argocd-context': '[{"name":"kargo-demo-uat","namespace":"argocd"}]' } },
  spec: {
    promotionTemplate,
    requestedFreight: [{ origin, sources: { stages: ['test'] } }],
    verification: { analysisTemplates: [{ name: 'smoke' }] },
  },
  status: {
    conditions: [
      { type: 'Ready', status: 'False', reason: 'VerificationFailed', message: UAT_VERIFY_ERROR },
      { type: 'Healthy', status: 'True', reason: 'Healthy', message: 'Stage is healthy (performed 1 health checks)' },
      { type: 'Verified', status: 'False', reason: 'VerificationFailed', message: UAT_VERIFY_ERROR },
    ],
    freightHistory: [
      {
        id: '7c6082af37571cf271f35aca5d5245afca02d72f',
        items: { 'Warehouse/kargo-demo': newRef },
        verificationHistory: [{
          analysisRun: { name: 'uat.01m89d5xvxsp57zytxjpy9c6ws.7c6082a', namespace: NS, phase: 'Failed' },
          finishTime: '2026-11-25T13:09:18Z',
          id: '28438234-9efc-4606-926f-722b27728f99',
          message: UAT_VERIFY_ERROR,
          phase: 'Failed',
          startTime: '2026-11-25T13:09:05Z',
        }],
      },
      { id: '7bbc8df2bedd6928df2f79b98f9e8cd449ac072a', items: { 'Warehouse/kargo-demo': oldRef } },
    ],
    health: {
      status: 'Healthy',
      output: [{ applicationStatuses: [{ Name: 'kargo-demo-uat', Namespace: 'argocd', health: { status: 'Healthy' }, sync: { revision: '438a08307acf2e0946ecc0239c86f2a1bbdfece6', status: 'Synced' } }] }],
    },
    lastPromotion: {
      finishedAt: '2026-11-25T13:09:05Z',
      freight: newRef,
      name: 'uat.01m89d5wygkj42skjy4ar9hg60.b56f6e3',
      status: { phase: 'Succeeded' },
    },
  },
}

export const prodStage: KargoStage = {
  apiVersion: API,
  kind: 'Stage',
  metadata: { name: 'prod', namespace: NS, annotations: { 'kargo.akuity.io/argocd-context': '[{"name":"kargo-demo-prod","namespace":"argocd"}]' } },
  spec: {
    promotionTemplate,
    requestedFreight: [{ origin, sources: { stages: ['uat'] } }],
  },
  status: {
    conditions: [
      { type: 'Ready', status: 'True', reason: 'Verified', message: 'Freight has been verified' },
      { type: 'Healthy', status: 'True', reason: 'Healthy', message: 'Stage is healthy (performed 1 health checks)' },
      { type: 'Verified', status: 'True', reason: 'Verified', message: 'Freight has been verified' },
    ],
    freightHistory: [{
      id: '7bbc8df2bedd6928df2f79b98f9e8cd449ac072a',
      items: { 'Warehouse/kargo-demo': oldRef },
      verificationHistory: [{ finishTime: '2026-11-25T13:08:45Z', id: 'e74b29b1-c316-4ee6-8cc7-19ca708766eb', phase: 'Successful', startTime: '2026-11-25T13:08:45Z' }],
    }],
    health: {
      status: 'Healthy',
      output: [{ applicationStatuses: [{ Name: 'kargo-demo-prod', Namespace: 'argocd', health: { status: 'Healthy' }, sync: { revision: 'd700e31f468a7197885b87a9b3fe8604010bd409', status: 'Synced' } }] }],
    },
    lastPromotion: {
      finishedAt: '2026-11-25T13:08:45Z',
      freight: oldRef,
      name: 'prod.01m89d58tnm1cymat6bvc5jwgw.c9e2bdd',
      status: { phase: 'Succeeded' },
    },
  },
}

const skipped = (alias: string) => ({ alias, status: 'Skipped' })

/** The latest promotion of test: errored at git push. */
export const erroredPromotion: KargoPromotion = {
  apiVersion: API,
  kind: 'Promotion',
  metadata: { name: 'test.01m89d6j01phpbye0vx2ftjhrh.b56f6e3', namespace: NS, labels: { 'kargo.akuity.io/stage': 'test' } },
  spec: {
    freight: NEW_FREIGHT,
    stage: 'test',
    steps: [
      { as: 'promo-process::step-1', uses: 'git-clone' },
      { as: 'promo-process::step-2', uses: 'git-clear' },
      { as: 'promo-process::update', uses: 'kustomize-set-image' },
      { as: 'promo-process::step-4', uses: 'kustomize-build' },
      { as: 'promo-process::commit', uses: 'git-commit' },
      { as: 'promo-process::step-6', uses: 'git-push' },
      { as: 'promo-process::step-7', uses: 'argocd-update' },
    ],
  },
  status: {
    currentStep: 6,
    finishedAt: '2026-11-25T13:09:27Z',
    freight: newRef,
    message: TEST_PUSH_ERROR,
    phase: 'Errored',
    startedAt: '2026-11-25T13:09:26Z',
    stepExecutionMetadata: [
      { alias: 'promo-process::step-1', status: 'Succeeded', startedAt: '2026-11-25T13:09:26Z', finishedAt: '2026-11-25T13:09:27Z' },
      { alias: 'promo-process::step-2', status: 'Succeeded', startedAt: '2026-11-25T13:09:27Z', finishedAt: '2026-11-25T13:09:27Z' },
      { alias: 'promo-process::update', status: 'Succeeded', startedAt: '2026-11-25T13:09:27Z', finishedAt: '2026-11-25T13:09:27Z' },
      { alias: 'promo-process::step-4', status: 'Succeeded', startedAt: '2026-11-25T13:09:27Z', finishedAt: '2026-11-25T13:09:27Z' },
      { alias: 'promo-process::commit', status: 'Skipped', startedAt: '2026-11-25T13:09:27Z', finishedAt: '2026-11-25T13:09:27Z' },
      { alias: 'promo-process::step-6', status: 'Errored', errorCount: 1, message: TEST_PUSH_ERROR, startedAt: '2026-11-25T13:09:27Z', finishedAt: '2026-11-25T13:09:27Z' },
      skipped('promo-process::step-7'),
    ],
  },
}

/** A prod promotion stopped by a fail step, superseded by a later successful one. */
export const failedPromotion: KargoPromotion = {
  apiVersion: API,
  kind: 'Promotion',
  metadata: { name: 'prod.01m89d581n87fzkj3we6wtv6mx.c9e2bdd', namespace: NS, labels: { 'kargo.akuity.io/stage': 'prod' } },
  spec: {
    freight: OLD_FREIGHT,
    stage: 'prod',
    steps: [
      { as: 'release-gate', uses: 'fail' },
      { as: 'promo-process::step-1', uses: 'git-clone' },
      { as: 'promo-process::step-2', uses: 'git-clear' },
    ],
  },
  status: {
    finishedAt: '2026-11-25T13:08:43Z',
    freight: oldRef,
    message: 'step "release-gate": failed: change freeze in effect for prod',
    phase: 'Failed',
    startedAt: '2026-11-25T13:08:43Z',
    stepExecutionMetadata: [
      { alias: 'release-gate', status: 'Failed', message: 'step "release-gate": failed: change freeze in effect for prod', startedAt: '2026-11-25T13:08:43Z', finishedAt: '2026-11-25T13:08:43Z' },
      skipped('promo-process::step-1'),
      skipped('promo-process::step-2'),
    ],
  },
}
