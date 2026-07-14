import { describe, it, expect } from 'vitest';
import { mapWebhook } from '../../src/evolution/webhook-mapper.js';

describe('mapWebhook', () => {
  it('connection open -> CONNECTED', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'open' } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'CONNECTED' } });
  });

  it('connection close 401 -> LOGGED_OUT', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'close', statusReason: 401 } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'LOGGED_OUT' } });
  });

  it('connection close other -> DISCONNECTED', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'close', statusReason: 500 } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'DISCONNECTED' } });
  });

  it('connecting -> ignored', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'connecting' } }))
      .toEqual({ kind: 'ignored' });
  });

  it('messages.update SERVER_ACK -> SENT', () => {
    expect(mapWebhook({ event: 'messages.update', instance: 'acct-1', data: { keyId: 'M1', status: 'SERVER_ACK' } }))
      .toEqual({ kind: 'message_status', instanceName: 'acct-1', providerMessageId: 'M1', status: 'SENT' });
  });

  it('messages.update READ -> READ', () => {
    expect(mapWebhook({ event: 'messages.update', instance: 'acct-1', data: { keyId: 'M1', status: 'READ' } }))
      .toEqual({ kind: 'message_status', instanceName: 'acct-1', providerMessageId: 'M1', status: 'READ' });
  });

  it('unknown event -> ignored', () => {
    expect(mapWebhook({ event: 'presence.update', instance: 'acct-1', data: {} })).toEqual({ kind: 'ignored' });
  });
});
