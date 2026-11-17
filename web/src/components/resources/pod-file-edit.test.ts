import { describe, expect, it } from 'vitest'
import { canShowEdit, editBlockedReason, lineEndings } from './pod-file-edit'

describe('canShowEdit', () => {
  it('needs exec and a Radar that serves the save endpoint', () => {
    expect(canShowEdit({ canExec: true, featureSupported: true })).toBe(true)
    expect(canShowEdit({ canExec: false, featureSupported: true })).toBe(false)
    expect(canShowEdit({ canExec: true, featureSupported: false })).toBe(false)
  })
})

describe('lineEndings', () => {
  it('names a file that uses one style throughout', () => {
    expect(lineEndings('')).toBe('none')
    expect(lineEndings('one line')).toBe('none')
    expect(lineEndings('a\nb\n')).toBe('lf')
    expect(lineEndings('a\r\nb\r\n')).toBe('crlf')
    expect(lineEndings('﻿a\r\nb')).toBe('crlf')
  })

  it('flags a mix, which the editor would rewrite one way', () => {
    expect(lineEndings('a\r\nb\nc')).toBe('mixed')
    expect(lineEndings('a\rb')).toBe('mixed')
    expect(lineEndings('a\r\nb\rc')).toBe('mixed')
  })
})

const pod = {
  spec: {
    containers: [
      {
        name: 'app',
        volumeMounts: [
          { name: 'cfg', mountPath: '/etc/cfg' },
          { name: 'one', mountPath: '/etc/nginx/nginx.conf' },
          { name: 'data', mountPath: '/data' },
        ],
      },
    ],
    initContainers: [{ name: 'sidecar', restartPolicy: 'Always', volumeMounts: [{ name: 'cfg', mountPath: '/config' }] }],
    volumes: [
      { name: 'cfg', configMap: { name: 'app-config' } },
      { name: 'one', configMap: { name: 'nginx' } },
      { name: 'data', emptyDir: {} },
    ],
  },
}

describe('editBlockedReason', () => {
  it('allows a file on a writable path with one line-ending style', () => {
    expect(editBlockedReason(pod, 'app', '/data/a.txt', 'x\ny\n')).toBeNull()
    expect(editBlockedReason(pod, 'app', '/data/a.txt', null)).toBeNull()
  })

  it('blocks files under a ConfigMap mount, native sidecars included', () => {
    expect(editBlockedReason(pod, 'app', '/etc/cfg/app.conf', 'x')).toMatch(/ConfigMap/)
    expect(editBlockedReason(pod, 'sidecar', '/config/app.conf', 'x')).toMatch(/ConfigMap/)
  })

  it('blocks a file mounted on its own', () => {
    expect(editBlockedReason(pod, 'app', '/etc/nginx/nginx.conf', 'x')).toMatch(/mounted into the container on its own/)
  })

  it('blocks a file with mixed line endings', () => {
    expect(editBlockedReason(pod, 'app', '/data/a.txt', 'a\r\nb\n')).toMatch(/mixes line endings/)
  })
})
