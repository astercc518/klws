import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaAccountRepository } from '../../src/adapters/prisma-account-repository.js';
import type { Account } from '../../src/domain/account.js';

const prisma = new PrismaClient();
const repo = new PrismaAccountRepository(prisma);

const acct: Account = {
  id: 'a1', phoneNumber: '5511999999999', state: 'CONNECTING',
  instanceName: 'acct-5511999999999',
  proxy: { host: '1.2.3.4', port: 8000, protocol: 'http' }, deadReason: null,
};

beforeEach(async () => {
  await prisma.message.deleteMany();
  await prisma.account.deleteMany();
});
afterAll(async () => { await prisma.$disconnect(); });

describe('PrismaAccountRepository', () => {
  it('saves and finds by id with proxy round-trip', async () => {
    await repo.save(acct);
    expect(await repo.findById('a1')).toEqual(acct);
  });

  it('finds by instance name', async () => {
    await repo.save(acct);
    expect(await repo.findByInstanceName('acct-5511999999999')).toEqual(acct);
  });

  it('upserts on repeated save (state change)', async () => {
    await repo.save(acct);
    await repo.save({ ...acct, state: 'DEAD', proxy: null, deadReason: 'LOGGED_OUT' });
    const found = await repo.findById('a1');
    expect(found!.state).toBe('DEAD');
    expect(found!.proxy).toBeNull();
    expect(found!.deadReason).toBe('LOGGED_OUT');
  });
});
