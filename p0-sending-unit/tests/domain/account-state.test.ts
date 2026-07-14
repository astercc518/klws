import { describe, it, expect } from 'vitest';
import { transition, InvalidTransitionError } from '../../src/domain/account-state.js';

describe('account state machine', () => {
  it('happy path: IMPORTED -> ... -> ONLINE', () => {
    let s = transition('IMPORTED', { type: 'BIND_PROXY' });
    expect(s).toBe('PROXY_BOUND');
    s = transition(s, { type: 'CONNECT' });
    expect(s).toBe('CONNECTING');
    s = transition(s, { type: 'CONNECTED' });
    expect(s).toBe('ONLINE');
  });

  it('ONLINE + DISCONNECTED -> RECONNECTING -> CONNECTED -> ONLINE', () => {
    expect(transition('ONLINE', { type: 'DISCONNECTED' })).toBe('RECONNECTING');
    expect(transition('RECONNECTING', { type: 'CONNECTED' })).toBe('ONLINE');
  });

  it('LOGGED_OUT from any live state -> DEAD (封号即弃)', () => {
    expect(transition('CONNECTING', { type: 'LOGGED_OUT' })).toBe('DEAD');
    expect(transition('ONLINE', { type: 'LOGGED_OUT' })).toBe('DEAD');
    expect(transition('RECONNECTING', { type: 'LOGGED_OUT' })).toBe('DEAD');
  });

  it('RETIRE from any state -> DEAD', () => {
    expect(transition('ONLINE', { type: 'RETIRE' })).toBe('DEAD');
  });

  it('rejects illegal transition', () => {
    expect(() => transition('DEAD', { type: 'CONNECT' })).toThrow(InvalidTransitionError);
    expect(() => transition('IMPORTED', { type: 'CONNECTED' })).toThrow(InvalidTransitionError);
  });
});
