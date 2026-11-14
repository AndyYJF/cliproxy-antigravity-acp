import { describe, expect, test } from 'bun:test';
import { isACPFrameMessage, resolveACPAction, resolveACPPayload } from '../src/features/plugins/acpManagementBridge';
describe('ACP management frame bridge', () => {
  test('only exposes fixed management operations', () => {
    expect(resolveACPAction('status')?.method).toBe('GET');
    expect(resolveACPAction('refresh-models')?.method).toBe('POST');
    for (const input of ['config', 'chat', 'https://evil.test', null, {}])
      expect(resolveACPAction(input)).toBeNull();
  });
  test('requires the selected ACP frame and its origin', () => {
    const frame = {};
    expect(
      isACPFrameMessage('antigravity-acp', frame, frame, 'https://cpa.test', 'https://cpa.test')
    ).toBe(true);
    expect(isACPFrameMessage('other', frame, frame, 'https://cpa.test', 'https://cpa.test')).toBe(
      false
    );
    expect(
      isACPFrameMessage('antigravity-acp', {}, frame, 'https://cpa.test', 'https://cpa.test')
    ).toBe(false);
    expect(
      isACPFrameMessage('antigravity-acp', frame, frame, 'https://evil.test', 'https://cpa.test')
    ).toBe(false);
    expect(
      isACPFrameMessage('antigravity-acp', null, null, 'https://cpa.test', 'https://cpa.test')
    ).toBe(false);
  });
});

test('account actions accept only their bounded payload', () => {
 expect(resolveACPAction('oauth-complete')?.path).toEndWith('/oauth/complete');
 expect(resolveACPPayload('oauth-complete',{id:'login_test',code:'test'})).toEqual({id:'login_test',code:'test'});
 expect(resolveACPPayload('oauth-complete',{id:'test',code:'x'.repeat(8193)})).toBeNull();
 expect(resolveACPPayload('select-account',{id:'test',url:'https://evil.test'})).toBeNull();
 expect(resolveACPPayload('oauth-status',{})).toEqual({});
});
