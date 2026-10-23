import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { FileNode } from '../../types'
import {
  canShowUpload,
  fileMountHint,
  isValidUploadName,
  oversizeMessage,
  POD_FILE_PERSISTENCE_NOTE,
  uploadBlockedHint,
  uploadTarget,
} from './pod-file-upload'
import { PodFileUploadButton, PodFileUploadNotices, PodFileUploadStatus } from './PodFileUploadPanel'

describe('canShowUpload', () => {
  it('needs exec and a server that serves the endpoint', () => {
    expect(canShowUpload({ canExec: true, featureSupported: true })).toBe(true)
    expect(canShowUpload({ canExec: false, featureSupported: true })).toBe(false)
    expect(canShowUpload({ canExec: true, featureSupported: false })).toBe(false)
  })
})

const pod = {
  spec: {
    containers: [
      {
        name: 'app',
        volumeMounts: [
          { name: 'cfg', mountPath: '/etc/app' },
          { name: 'tls', mountPath: '/etc/tls/' },
          { name: 'data', mountPath: '/data' },
          { name: 'shared', mountPath: '/shared', readOnly: true },
          { name: 'one', mountPath: '/etc/nginx/nginx.conf' },
          { name: 'proj', mountPath: '/var/run/proj' },
        ],
      },
      { name: 'locked', securityContext: { readOnlyRootFilesystem: true }, volumeMounts: [{ name: 'data', mountPath: '/data' }] },
    ],
    initContainers: [
      { name: 'sidecar', restartPolicy: 'Always', volumeMounts: [{ name: 'cfg', mountPath: '/config' }] },
    ],
    volumes: [
      { name: 'cfg', configMap: { name: 'app-config' } },
      { name: 'tls', secret: { secretName: 'app-tls' } },
      { name: 'data', emptyDir: {} },
      { name: 'shared', persistentVolumeClaim: { claimName: 'shared' } },
      { name: 'one', configMap: { name: 'nginx' } },
      { name: 'proj', projected: { sources: [{ secret: { name: 'only-one' } }] } },
    ],
  },
}

describe('uploadBlockedHint', () => {
  it('blocks ConfigMap and Secret mounts and names their source', () => {
    expect(uploadBlockedHint(pod, 'app', '/etc/app')?.source).toEqual({ kind: 'ConfigMap', name: 'app-config' })
    expect(uploadBlockedHint(pod, 'app', '/etc/app/..data')?.source).toEqual({ kind: 'ConfigMap', name: 'app-config' })
    expect(uploadBlockedHint(pod, 'app', '/etc/tls')?.source).toEqual({ kind: 'Secret', name: 'app-tls' })
  })

  it('names the single source of a projected volume', () => {
    expect(uploadBlockedHint(pod, 'app', '/var/run/proj')?.source).toEqual({ kind: 'Secret', name: 'only-one' })
  })

  it('blocks read-only mounts', () => {
    expect(uploadBlockedHint(pod, 'app', '/shared/x')?.reason).toMatch(/read-only/)
  })

  it('allows writable volumes and an unmounted path on a writable root', () => {
    expect(uploadBlockedHint(pod, 'app', '/data')).toBeNull()
    expect(uploadBlockedHint(pod, 'app', '/tmp')).toBeNull()
    expect(uploadBlockedHint(pod, 'app', '/etc')).toBeNull()
    expect(uploadBlockedHint(pod, 'app', '/etc/application')).toBeNull()
  })

  it('blocks a read-only root filesystem outside writable volumes', () => {
    expect(uploadBlockedHint(pod, 'locked', '/tmp')?.reason).toMatch(/readOnlyRootFilesystem/)
    expect(uploadBlockedHint(pod, 'locked', '/data/sub')).toBeNull()
  })

  it('reads mounts of native sidecars', () => {
    expect(uploadBlockedHint(pod, 'sidecar', '/config')?.source).toEqual({ kind: 'ConfigMap', name: 'app-config' })
  })

  it('gives no hint for an unknown container or missing pod', () => {
    expect(uploadBlockedHint(pod, 'nope', '/etc/app')).toBeNull()
    expect(uploadBlockedHint(undefined, 'app', '/etc/app')).toBeNull()
  })
})

describe('fileMountHint', () => {
  it('flags a file mounted on its own', () => {
    const hint = fileMountHint(pod, 'app', '/etc/nginx/nginx.conf')
    expect(hint?.source).toEqual({ kind: 'ConfigMap', name: 'nginx' })
    expect(fileMountHint(pod, 'app', '/etc/nginx/other.conf')).toBeNull()
  })
})

describe('uploadTarget', () => {
  const listing: FileNode = {
    name: 'etc',
    path: '/etc',
    type: 'dir',
    children: [
      { name: 'a.conf', path: '/etc/a.conf', type: 'file' },
      { name: 'conf.d', path: '/etc/conf.d', type: 'dir' },
      { name: 'link', path: '/etc/link', type: 'symlink', linkTarget: '/x' },
    ],
  }

  it('asks before replacing an existing file', () => {
    expect(uploadTarget(listing, 'a.conf')).toEqual({ kind: 'replace' })
  })

  it('uploads a new name straight away', () => {
    expect(uploadTarget(listing, 'b.conf')).toEqual({ kind: 'new' })
  })

  it('refuses directories and symlinks', () => {
    expect(uploadTarget(listing, 'conf.d').kind).toBe('refuse')
    expect(uploadTarget(listing, 'link').kind).toBe('refuse')
  })
})

describe('isValidUploadName', () => {
  it('matches what the server accepts', () => {
    expect(isValidUploadName('app.conf')).toBe(true)
    expect(isValidUploadName('-rf')).toBe(true)
    for (const bad of ['', '.', '..', 'a/b', 'a\0b', 'x'.repeat(256)]) {
      expect(isValidUploadName(bad)).toBe(false)
    }
  })
})

describe('oversizeMessage', () => {
  it('refuses a file over the advertised cap before sending it', () => {
    expect(oversizeMessage(200 * 1024 * 1024)).toMatch(/limited to 100/)
    expect(oversizeMessage(100 * 1024 * 1024)).toBeNull()
    expect(oversizeMessage(10, 5)).toMatch(/limited to/)
  })
})

describe('upload controls', () => {
  it('disables the button under a known read-only mount and says why', () => {
    const html = renderToStaticMarkup(
      <PodFileUploadButton blocked={{ reason: '/etc/app is a ConfigMap volume.' }} busy={false} onClick={() => {}} />,
    )
    expect(html).toMatch(/<button[^>]* disabled=""/)
  })

  it('disables the button while the listing for the selected container loads', () => {
    const html = renderToStaticMarkup(<PodFileUploadButton blocked={null} busy={false} loading onClick={() => {}} />)
    expect(html).toMatch(/<button[^>]* disabled=""/)
  })

  it('enables the button when nothing blocks it', () => {
    const html = renderToStaticMarkup(<PodFileUploadButton blocked={null} busy={false} onClick={() => {}} />)
    expect(html).not.toMatch(/<button[^>]* disabled=""/)
  })

  it('shows the persistence note verbatim and links to the blocking source', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <PodFileUploadNotices
          namespace="prod"
          blocked={{ reason: 'ConfigMap volume', source: { kind: 'ConfigMap', name: 'app-config' } }}
          onNavigateAway={() => {}}
        />
      </MemoryRouter>,
    )
    expect(html).toContain(POD_FILE_PERSISTENCE_NOTE.replace(/'/g, '&#x27;'))
    expect(html).toContain('href="/resources/configmaps?resource=prod%2Fapp-config"')
  })

  it('asks for confirmation before replacing', () => {
    const html = renderToStaticMarkup(
      <PodFileUploadStatus
        state={{ phase: 'confirm', name: 'a.conf', dir: '/etc', size: 10 }}
        onConfirmReplace={() => {}}
        onCancel={() => {}}
        onDismiss={() => {}}
      />,
    )
    expect(html).toContain('Replace a.conf?')
    expect(html).toContain('/etc/a.conf already exists')
    expect(html).toContain('>Replace<')
  })

  it('keeps uploading and saved distinct', () => {
    const uploading = renderToStaticMarkup(
      <PodFileUploadStatus state={{ phase: 'uploading', name: 'a', dir: '/', sent: 50, total: 100 }} onConfirmReplace={() => {}} onCancel={() => {}} onDismiss={() => {}} />,
    )
    expect(uploading).toContain('aria-valuenow="50"')
    expect(uploading).toContain('>Cancel<')
    const saving = renderToStaticMarkup(
      <PodFileUploadStatus state={{ phase: 'saving', name: 'a', dir: '/' }} onConfirmReplace={() => {}} onCancel={() => {}} onDismiss={() => {}} />,
    )
    expect(saving).toContain('Saving a')
    expect(saving).not.toContain('>Cancel<')
    const saved = renderToStaticMarkup(
      <PodFileUploadStatus state={{ phase: 'saved', path: '/a', replaced: false }} onConfirmReplace={() => {}} onCancel={() => {}} onDismiss={() => {}} />,
    )
    expect(saved).toContain('Saved /a')
  })
})
