import { useId, useState } from 'react'
import { Input } from '@skyhook-io/k8s-ui'
import { scheduleExpression, simpleSchedule, type SimpleSchedule } from './scheduleModel'
import { CNPG_FORM_FIELD as FIELD } from '../formFields'

export function CNPGScheduleInput({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const id = useId()
  const simple = simpleSchedule(value)
  const [advanced, setAdvanced] = useState(!simple)
  const selected = advanced || !simple ? 'advanced' : simple.frequency
  const set = (next: Partial<SimpleSchedule>) =>
    onChange(scheduleExpression({ frequency: 'daily', time: '02:00', day: '1', ...simple, ...next }))
  return (
    <div className="space-y-2">
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="block">
          <span className="text-xs font-medium text-theme-text-secondary">Frequency</span>
          <select
            id={`${id}-frequency`}
            aria-label="Backup frequency"
            className={FIELD}
            value={selected}
            onChange={(event) => {
              const frequency = event.target.value
              setAdvanced(frequency === 'advanced')
              if (frequency !== 'advanced')
                set({
                  frequency: frequency as SimpleSchedule['frequency'],
                  day: frequency === 'weekly' ? '1' : simple?.frequency === 'monthly' ? simple.day : '1',
                })
            }}
          >
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
            <option value="monthly">Monthly</option>
            <option value="advanced">Advanced cron</option>
          </select>
        </label>
        {selected !== 'advanced' && (
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">Time · operator clock</span>
            <Input
              type="time"
              className={FIELD}
              value={simple?.time ?? '02:00'}
              onChange={(event) => {
                if (event.target.value) set({ time: event.target.value })
              }}
            />
          </label>
        )}
        {selected === 'weekly' && (
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">Day</span>
            <select
              aria-label="Backup weekday"
              className={FIELD}
              value={simple?.day}
              onChange={(event) => set({ day: event.target.value })}
            >
              {['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'].map((day, index) => (
                <option key={day} value={index}>
                  {day}
                </option>
              ))}
            </select>
          </label>
        )}
        {selected === 'monthly' && (
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">Day of month</span>
            <select
              aria-label="Backup day of month"
              className={FIELD}
              value={simple?.day}
              onChange={(event) => set({ day: event.target.value })}
            >
              {Array.from({ length: 31 }, (_, index) => (
                <option key={index} value={index + 1}>
                  {index + 1}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      {selected === 'advanced' ? (
        <label className="block">
          <span className="text-xs font-medium text-theme-text-secondary">
            Schedule · seconds first, or a descriptor such as @daily
          </span>
          <Input
            id="cnpg-schedule-input"
            className={`${FIELD} font-mono`}
            value={value}
            onChange={(event) => onChange(event.target.value)}
            spellCheck={false}
            autoComplete="off"
          />
        </label>
      ) : (
        <div className="font-mono text-xs text-theme-text-tertiary">{value}</div>
      )}
      {selected === 'monthly' && Number(simple?.day) > 28 && (
        <p className="text-xs text-theme-text-secondary">
          Months without day {simple?.day} are skipped; no backup is scheduled for that month.
        </p>
      )}
      <p className="text-xs text-theme-text-tertiary">
        Times use the operator’s clock, not your browser’s. The server preview below states which clock is known or
        assumed.
      </p>
    </div>
  )
}
