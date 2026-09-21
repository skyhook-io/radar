import { describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { DiagnoseCustomizationProvider, useDiagnoseCustomization } from './DiagnoseCustomization';
import type { RunSummary } from '../api/diagnose';

const run = { id: 'r1' } as RunSummary;
const onRunUpdated = vi.fn();
function Actions() {
  const { renderRunActions } = useDiagnoseCustomization();
  return <>{renderRunActions?.({ run, onRunUpdated })}</>;
}

describe('investigation action slot', () => {
  it('has no default host actions in OSS', () => {
    expect(renderToStaticMarkup(<Actions />)).toBe('');
  });
  it('passes the run and update callback to the host', () => {
    const renderRunActions = vi.fn(() => <button>Host action</button>);
    expect(renderToStaticMarkup(
      <DiagnoseCustomizationProvider value={undefined} renderRunActions={renderRunActions}>
        <Actions />
      </DiagnoseCustomizationProvider>,
    )).toContain('Host action');
    expect(renderRunActions).toHaveBeenCalledWith({ run, onRunUpdated });
  });
});
