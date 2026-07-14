import { describe, it, expect, vi } from 'vitest';
import { AccountService } from '../../src/pool/account-service.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import { InMemoryMessageRepository } from '../../src/adapters/in-memory-message-repository.js';
import type { Proxy } from '../../src/domain/account.js';

const proxy: Proxy = { host: '1.2.3.4', port: 8000, protocol: 'http' };

function makeIds() {
  let n = 0;
  return { next: () => `id-${++n}` };
}

function makeService() {
  const accounts = new InMemoryAccountRepository();
  const messages = new InMemoryMessageRepository();
  const evolution = {
    createInstance: vi.fn(async () => {}),
    connect: vi.fn(async () => ({ pairingCode: 'PAIR-1234' })),
    sendText: vi.fn(async () => ({ providerMessageId: 'PMID-1' })),
  };
  const svc = new AccountService({
    accounts, messages,
    evolution: evolution as never,
    webhookUrl: 'http://w/hook',
    ids: makeIds(),
  });
  return { svc, accounts, messages, evolution };
}

describe('AccountService', () => {
  it('importAccount creates an IMPORTED account with derived instance name', async () => {
    const { svc } = makeService();
    const a = await svc.importAccount('5511999999999');
    expect(a.state).toBe('IMPORTED');
    expect(a.instanceName).toBe('acct-5511999999999');
    expect(a.proxy).toBeNull();
  });

  it('bringOnline binds proxy, creates instance, connects, moves to CONNECTING', async () => {
    const { svc, accounts, evolution } = makeService();
    const a = await svc.importAccount('5511999999999');
    const r = await svc.bringOnline(a.id, proxy);

    expect(r.pairingCode).toBe('PAIR-1234');
    expect(evolution.createInstance).toHaveBeenCalledOnce();
    expect(evolution.connect).toHaveBeenCalledWith('acct-5511999999999');
    const saved = await accounts.findById(a.id);
    expect(saved!.state).toBe('CONNECTING');
    expect(saved!.proxy).toEqual(proxy);
  });

  it('sendMessage requires ONLINE state', async () => {
    const { svc } = makeService();
    const a = await svc.importAccount('5511999999999');
    await expect(svc.sendMessage(a.id, '5511888888888', 'oi')).rejects.toThrow(/ONLINE/);
  });

  it('CONNECTED webhook -> account ONLINE, then sendMessage persists PENDING message with providerMessageId', async () => {
    const { svc, messages, evolution } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });

    const m = await svc.sendMessage(a.id, '5511888888888', 'oi');
    expect(evolution.sendText).toHaveBeenCalledWith({ instanceName: 'acct-5511999999999', to: '5511888888888', text: 'oi' });
    expect(m.status).toBe('PENDING');
    expect(m.providerMessageId).toBe('PMID-1');
    const found = await messages.findByProviderMessageId('PMID-1');
    expect(found!.id).toBe(m.id);
  });

  it('messages.update webhook updates message status', async () => {
    const { svc, messages } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });
    await svc.sendMessage(a.id, '5511888888888', 'oi');

    await svc.handleWebhook({ event: 'messages.update', instance: 'acct-5511999999999', data: { keyId: 'PMID-1', status: 'DELIVERY_ACK' } });
    const found = await messages.findByProviderMessageId('PMID-1');
    expect(found!.status).toBe('DELIVERED');
  });

  it('LOGGED_OUT webhook -> account DEAD, proxy released, deadReason set (封号即弃)', async () => {
    const { svc, accounts } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'close', statusReason: 401 } });

    const saved = await accounts.findById(a.id);
    expect(saved!.state).toBe('DEAD');
    expect(saved!.proxy).toBeNull();
    expect(saved!.deadReason).toBe('LOGGED_OUT');
  });
});
