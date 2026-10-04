import { describe, expect, it } from 'vitest'
import { cnpgArchiveDestination } from './archivingRepair'

describe('cnpgArchiveDestination', () => {
  it('names each credential Secret and key, never reading them', () => {
    const d = cnpgArchiveDestination(
      {
        destinationPath: 's3://radar-cnpg-demo/',
        endpointURL: 'http://192.0.2.1:9000',
        s3Credentials: { accessKeyId: { name: 'creds', key: 'ACCESS_KEY_ID' }, secretAccessKey: { name: 'creds', key: 'ACCESS_SECRET_KEY' } },
        endpointCA: { name: 'minio-ca', key: 'ca.crt' },
      },
      'ObjectStore pg-store',
    )
    expect(d).toMatchObject({ path: 's3://radar-cnpg-demo/', endpointURL: 'http://192.0.2.1:9000', declaredIn: 'ObjectStore pg-store' })
    expect(d.secrets).toEqual([
      { secret: 'creds', key: 'ACCESS_KEY_ID', what: 'S3 access key ID' },
      { secret: 'creds', key: 'ACCESS_SECRET_KEY', what: 'S3 secret access key' },
      { secret: 'minio-ca', key: 'ca.crt', what: 'endpoint CA bundle' },
    ])
    expect(d.identity).toBeUndefined()
  })
  it('says when credentials come from the workload identity instead', () => {
    expect(cnpgArchiveDestination({ destinationPath: 's3://b/', s3Credentials: { inheritFromIAMRole: true } }, 'x').identity).toContain('IAM role')
    expect(cnpgArchiveDestination({ destinationPath: 'gs://b/', googleCredentials: { gkeEnvironment: true } }, 'x').identity).toContain('GKE')
  })
})
