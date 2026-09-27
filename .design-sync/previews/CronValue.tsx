import { CronValue, Property, PropertyList } from '@skyhook-io/k8s-ui'

const box = { width: 380 } as const

export function CronJobSchedules() {
  return (
    <div style={box}>
      <PropertyList>
        <Property label="Nightly backup" value={<CronValue cron="0 2 * * *" />} />
        <Property label="Cache warmer" value={<CronValue cron="*/15 * * * *" />} />
        <Property label="Weekly report" value={<CronValue cron="0 9 * * 1" />} />
      </PropertyList>
    </div>
  )
}

export function NoPlainEnglishReading() {
  return (
    <div style={box}>
      <PropertyList>
        <Property label="Cron" value={<CronValue cron="30 4 1,15 * *" />} />
      </PropertyList>
    </div>
  )
}

export function CloudNativePGSecondsDialect() {
  return (
    <div style={box}>
      <PropertyList>
        <Property label="Cron Expression" value={<CronValue cron="0 0 2 * * *" dialect="seconds" />} />
      </PropertyList>
    </div>
  )
}
