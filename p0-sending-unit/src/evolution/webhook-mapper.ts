import type { AccountEvent } from '../domain/account-state.js';
import type { MessageStatus } from '../domain/message.js';

export interface WebhookPayload {
  event: string;
  instance: string;
  data: Record<string, unknown>;
}

export type WebhookResult =
  | { kind: 'account_event'; instanceName: string; event: AccountEvent }
  | { kind: 'message_status'; instanceName: string; providerMessageId: string; status: MessageStatus }
  | { kind: 'ignored' };

const STATUS_MAP: Record<string, MessageStatus> = {
  SERVER_ACK: 'SENT',
  DELIVERY_ACK: 'DELIVERED',
  READ: 'READ',
  PLAYED: 'READ',
};

export function mapWebhook(p: WebhookPayload): WebhookResult {
  const event = p.event.toLowerCase();

  if (event === 'connection.update') {
    const state = p.data['state'];
    if (state === 'open') {
      return { kind: 'account_event', instanceName: p.instance, event: { type: 'CONNECTED' } };
    }
    if (state === 'close') {
      const reason = p.data['statusReason'];
      const type = reason === 401 ? 'LOGGED_OUT' : 'DISCONNECTED';
      return { kind: 'account_event', instanceName: p.instance, event: { type } };
    }
    return { kind: 'ignored' };
  }

  if (event === 'messages.update') {
    const pid = p.data['keyId'];
    const status = STATUS_MAP[String(p.data['status'])];
    if (typeof pid === 'string' && status) {
      return { kind: 'message_status', instanceName: p.instance, providerMessageId: pid, status };
    }
    return { kind: 'ignored' };
  }

  return { kind: 'ignored' };
}
