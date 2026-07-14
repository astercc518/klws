export type AccountState =
  | 'IMPORTED' | 'PROXY_BOUND' | 'CONNECTING' | 'ONLINE' | 'RECONNECTING' | 'DEAD';

export type AccountEvent =
  | { type: 'BIND_PROXY' }
  | { type: 'CONNECT' }
  | { type: 'CONNECTED' }
  | { type: 'DISCONNECTED' }
  | { type: 'LOGGED_OUT' }
  | { type: 'RETIRE' };

export class InvalidTransitionError extends Error {
  constructor(state: AccountState, event: AccountEvent['type']) {
    super(`Invalid transition: ${event} from ${state}`);
    this.name = 'InvalidTransitionError';
  }
}

const TABLE: Record<AccountState, Partial<Record<AccountEvent['type'], AccountState>>> = {
  IMPORTED:     { BIND_PROXY: 'PROXY_BOUND', RETIRE: 'DEAD' },
  PROXY_BOUND:  { CONNECT: 'CONNECTING', RETIRE: 'DEAD' },
  CONNECTING:   { CONNECTED: 'ONLINE', DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  ONLINE:       { DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  RECONNECTING: { CONNECTED: 'ONLINE', DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  DEAD:         {},
};

export function transition(state: AccountState, event: AccountEvent): AccountState {
  const next = TABLE[state][event.type];
  if (!next) throw new InvalidTransitionError(state, event.type);
  return next;
}
