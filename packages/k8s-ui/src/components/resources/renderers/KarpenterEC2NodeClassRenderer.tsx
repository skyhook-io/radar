import { Server, HardDrive, Shield, Network } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { getEC2NodeClassStatus } from '../resource-utils-karpenter'

interface KarpenterEC2NodeClassRendererProps {
  data: any
}

export function KarpenterEC2NodeClassRenderer({ data }: KarpenterEC2NodeClassRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const nodeClassStatus = getEC2NodeClassStatus(data)
  const isNotReady = nodeClassStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const amiTerms = spec.amiSelectorTerms || []
  const blockDevices = spec.blockDeviceMappings || []
  const subnetTerms = spec.subnetSelectorTerms || []
  const sgTerms = spec.securityGroupSelectorTerms || []
  const metadataOptions = spec.metadataOptions

  return (
    <>
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="EC2NodeClass Not Ready"
          message={readyCond?.message || 'The EC2NodeClass is not in a ready state.'}
        />
      )}

      {/* IAM & AMI */}
      <Section title="Instance Configuration" icon={Server}>
        <PropertyList>
          {spec.role && <Property label="IAM Role" value={spec.role} />}
          {amiTerms.length > 0 && (
            <Property
              label="AMI Selector"
              value={amiTerms.map((t: any) => t.alias || t.id || t.name || JSON.stringify(t)).join(', ')}
            />
          )}
          {spec.amiFamily && <Property label="AMI Family" value={spec.amiFamily} />}
        </PropertyList>
      </Section>

      {/* Block Devices */}
      {blockDevices.length > 0 && (
        <Section title={`Block Devices (${blockDevices.length})`} icon={HardDrive}>
          <div className="space-y-2">
            {blockDevices.map((bd: any, i: number) => (
              <div key={i} className="card-inner">
                <div className="text-sm font-medium text-theme-text-primary mb-1">
                  {bd.deviceName || `/dev/xvda`}
                </div>
                <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
                  {bd.ebs?.volumeType && (
                    <div>
                      <span className="text-theme-text-tertiary">Type: </span>
                      <span className="text-theme-text-secondary">{bd.ebs.volumeType}</span>
                    </div>
                  )}
                  {bd.ebs?.volumeSize && (
                    <div>
                      <span className="text-theme-text-tertiary">Size: </span>
                      <span className="text-theme-text-secondary">{bd.ebs.volumeSize}</span>
                    </div>
                  )}
                  {bd.ebs?.iops && (
                    <div>
                      <span className="text-theme-text-tertiary">IOPS: </span>
                      <span className="text-theme-text-secondary">{bd.ebs.iops}</span>
                    </div>
                  )}
                  {bd.ebs?.throughput && (
                    <div>
                      <span className="text-theme-text-tertiary">Throughput: </span>
                      <span className="text-theme-text-secondary">{bd.ebs.throughput}</span>
                    </div>
                  )}
                  <div>
                    <span className="text-theme-text-tertiary">Encrypted: </span>
                    <span className="text-theme-text-secondary">{bd.ebs?.encrypted ? 'Yes' : 'No'}</span>
                  </div>
                  {bd.ebs?.deleteOnTermination !== undefined && (
                    <div>
                      <span className="text-theme-text-tertiary">Delete on Termination: </span>
                      <span className="text-theme-text-secondary">{bd.ebs.deleteOnTermination ? 'Yes' : 'No'}</span>
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Network - Subnets & Security Groups */}
      <Section title="Network" icon={Network}>
        {subnetTerms.length > 0 && (
          <div className="mb-3">
            <div className="text-xs text-theme-text-tertiary font-medium mb-1">Subnet Selector</div>
            {subnetTerms.map((term: any, i: number) => (
              <div key={i} className="card-inner mb-1">
                {term.tags && Object.entries(term.tags).map(([key, val]) => (
                  <span key={key} className="badge-sm mr-1 mb-1 bg-theme-hover text-theme-text-secondary">
                    {key}: {String(val)}
                  </span>
                ))}
                {term.id && (
                  <span className="text-xs text-theme-text-secondary">{term.id}</span>
                )}
              </div>
            ))}
          </div>
        )}
        {sgTerms.length > 0 && (
          <div>
            <div className="text-xs text-theme-text-tertiary font-medium mb-1">Security Group Selector</div>
            {sgTerms.map((term: any, i: number) => (
              <div key={i} className="card-inner mb-1">
                {term.tags && Object.entries(term.tags).map(([key, val]) => (
                  <span key={key} className="badge-sm mr-1 mb-1 bg-theme-hover text-theme-text-secondary">
                    {key}: {String(val)}
                  </span>
                ))}
                {term.id && (
                  <span className="text-xs text-theme-text-secondary">{term.id}</span>
                )}
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Metadata Options */}
      {metadataOptions && (
        <Section title="Metadata Options" icon={Shield}>
          <PropertyList>
            {metadataOptions.httpTokens && (
              <Property label="HTTP Tokens (IMDSv2)" value={metadataOptions.httpTokens} />
            )}
            {metadataOptions.httpPutResponseHopLimit !== undefined && (
              <Property label="Hop Limit" value={String(metadataOptions.httpPutResponseHopLimit)} />
            )}
            {metadataOptions.httpEndpoint && (
              <Property label="HTTP Endpoint" value={metadataOptions.httpEndpoint} />
            )}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
