import { transition } from '../domain/account-state.js';
import { newInstanceName, type Account, type Proxy } from '../domain/account.js';
import type { Message } from '../domain/message.js';
import type { AccountRepository } from '../ports/account-repository.js';
import type { MessageRepository } from '../ports/message-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';
import { mapWebhook, type WebhookPayload } from '../evolution/webhook-mapper.js';

export interface IdGen { next(): string }

export class AccountService {
  constructor(
    private deps: {
      accounts: AccountRepository;
      messages: MessageRepository;
      evolution: EvolutionClient;
      webhookUrl: string;
      ids: IdGen;
    },
  ) {}

  async importAccount(phoneNumber: string): Promise<Account> {
    const account: Account = {
      id: this.deps.ids.next(),
      phoneNumber,
      state: 'IMPORTED',
      instanceName: newInstanceName(phoneNumber),
      proxy: null,
      deadReason: null,
    };
    await this.deps.accounts.save(account);
    return account;
  }

  async bringOnline(accountId: string, proxy: Proxy): Promise<{ pairingCode: string | null }> {
    const account = await this.require(accountId);
    // Validate both transitions up front, but do NOT persist until Evolution accepts the
    // instance — a failed create/connect then leaves the account in its prior state (retryable),
    // instead of stranding it in PROXY_BOUND with no valid transition back.
    const proxyBound = transition(account.state, { type: 'BIND_PROXY' });
    const connecting = transition(proxyBound, { type: 'CONNECT' });

    await this.deps.evolution.createInstance({
      instanceName: account.instanceName,
      number: account.phoneNumber,
      proxy,
      webhookUrl: this.deps.webhookUrl,
    });
    const connectResult = await this.deps.evolution.connect(account.instanceName);

    await this.deps.accounts.save({ ...account, proxy, state: connecting });
    return connectResult;
  }

  async sendMessage(accountId: string, to: string, text: string): Promise<Message> {
    const account = await this.require(accountId);
    if (account.state !== 'ONLINE') {
      throw new Error(`Account ${accountId} not ONLINE (state=${account.state})`);
    }
    const { providerMessageId } = await this.deps.evolution.sendText({
      instanceName: account.instanceName, to, text,
    });
    const message: Message = {
      id: this.deps.ids.next(),
      accountId,
      to,
      providerMessageId,
      status: 'PENDING',
    };
    await this.deps.messages.save(message);
    return message;
  }

  async handleWebhook(payload: WebhookPayload): Promise<void> {
    const result = mapWebhook(payload);
    if (result.kind === 'ignored') return;

    if (result.kind === 'message_status') {
      const msg = await this.deps.messages.findByProviderMessageId(result.providerMessageId);
      if (msg) await this.deps.messages.updateStatus(msg.id, result.status);
      return;
    }

    const account = await this.deps.accounts.findByInstanceName(result.instanceName);
    if (!account) return;
    const nextState = transition(account.state, result.event);
    const updated: Account = {
      ...account,
      state: nextState,
      proxy: nextState === 'DEAD' ? null : account.proxy,
      deadReason: nextState === 'DEAD' ? result.event.type : account.deadReason,
    };
    await this.deps.accounts.save(updated);
  }

  private async require(accountId: string): Promise<Account> {
    const account = await this.deps.accounts.findById(accountId);
    if (!account) throw new Error(`Account not found: ${accountId}`);
    return account;
  }
}
