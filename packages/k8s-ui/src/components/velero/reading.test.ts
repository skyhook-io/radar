import { describe, expect, it } from 'vitest'
import { veleroPlainReason } from './reading'

describe('veleroPlainReason', () => {
  it('says what the common object-store failures mean', () => {
    expect(veleroPlainReason('BackupStorageLocation "dr" is unavailable: rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist\n\tstatus code: 404')).toBe('The bucket doesn’t exist')
    expect(veleroPlainReason('rpc error: code = Unknown desc = AccessDenied: Access Denied\n\tstatus code: 403, request id: X')).toBe('Velero’s credentials can’t access the bucket')
    expect(veleroPlainReason('operation error S3: ListObjectsV2, https response error StatusCode: 403, api error Forbidden')).toBe('Velero’s credentials can’t access the bucket')
    expect(veleroPlainReason('InvalidAccessKeyId: The AWS Access Key Id you provided does not exist in our records.\n\tstatus code: 403')).toBe('Velero’s credentials are wrong')
    expect(veleroPlainReason('SignatureDoesNotMatch: The request signature we calculated does not match')).toBe('Velero’s credentials are wrong')
    expect(veleroPlainReason('dial tcp: lookup minio.velero.svc on 10.0.0.10:53: no such host')).toBe('Velero can’t reach the object store')
    expect(veleroPlainReason('Get "https://s3.example.com/bucket": dial tcp 10.1.2.3:443: i/o timeout')).toBe('Velero can’t reach the object store')
  })

  it('names a run Velero failed because it restarted', () => {
    expect(veleroPlainReason('found a backup with status "InProgress" during the server starting, mark it as "Failed"')).toBe('Velero restarted while this backup ran, so it was marked Failed — run it again')
    expect(veleroPlainReason('found a restore with status "InProgress" during the server starting, mark it as "Failed"')).toBe('Velero restarted while this restore ran, so it was marked Failed — run it again')
  })

  it('leaves a message it does not recognise alone', () => {
    expect(veleroPlainReason(undefined)).toBeUndefined()
    expect(veleroPlainReason('')).toBeUndefined()
    expect(veleroPlainReason('Invalid included/excluded namespace lists: excludes list cannot contain an item in the includes list: demo')).toBeUndefined()
  })
})
