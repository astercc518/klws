import type { AccountState } from './account-state.js';

export interface Proxy {
  host: string;
  port: number;
  protocol: 'http' | 'socks5';
  username?: string;
  password?: string;
}

export interface Account {
  id: string;
  phoneNumber: string;
  state: AccountState;
  instanceName: string;
  proxy: Proxy | null;
  deadReason: string | null;
}

export function newInstanceName(phoneNumber: string): string {
  return `acct-${phoneNumber}`;
}
