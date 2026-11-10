export type SimpleSchedule = { frequency: 'daily' | 'weekly' | 'monthly'; time: string; day: string }

export function simpleSchedule(value: string): SimpleSchedule | null {
  const match = value.match(/^0 (\d|[1-5]\d) (\d|1\d|2[0-3]) (\*|[1-9]|[12]\d|3[01]) \* (\*|[0-6])$/)
  if (!match || (match[3] !== '*' && match[4] !== '*')) return null
  return {
    frequency: match[3] !== '*' ? 'monthly' : match[4] !== '*' ? 'weekly' : 'daily',
    time: `${match[2].padStart(2, '0')}:${match[1].padStart(2, '0')}`,
    day: match[3] !== '*' ? match[3] : match[4] !== '*' ? match[4] : '1',
  }
}

export function scheduleExpression(value: SimpleSchedule): string {
  const [hour, minute] = value.time.split(':').map(Number)
  return `0 ${minute} ${hour} ${value.frequency === 'monthly' ? value.day : '*'} * ${value.frequency === 'weekly' ? value.day : '*'}`
}
