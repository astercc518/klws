import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaTargetNumberRepository } from '../../src/adapters/prisma-target-number-repository.js';
import type { TargetNumber } from '../../src/domain/target-number.js';

const prisma = new PrismaClient();
const repo = new PrismaTargetNumberRepository(prisma);

beforeEach(async () => { await prisma.targetNumber.deleteMany(); });
afterAll(async () => { await prisma.$disconnect(); });

const row: TargetNumber = { id: 'n1', raw: '+55 11 98765-4321', e164: '5511987654321', status: 'PENDING_CHECK', jid: null };

describe('PrismaTargetNumberRepository', () => {
  it('saves and finds by e164', async () => {
    await repo.save(row);
    expect(await repo.findByE164('5511987654321')).toEqual(row);
    expect(await repo.findByE164('0000')).toBeNull();
  });

  it('upserts status + jid on repeated save', async () => {
    await repo.save(row);
    await repo.save({ ...row, status: 'ON_WHATSAPP', jid: '5511987654321@s.whatsapp.net' });
    const found = await repo.findByE164('5511987654321');
    expect(found!.status).toBe('ON_WHATSAPP');
    expect(found!.jid).toBe('5511987654321@s.whatsapp.net');
  });

  it('listByStatus filters and respects limit', async () => {
    await repo.save({ ...row, id: 'a', e164: '5511111111111', status: 'PENDING_CHECK' });
    await repo.save({ ...row, id: 'b', e164: '5511222222222', status: 'PENDING_CHECK' });
    await repo.save({ ...row, id: 'c', e164: '5511333333333', status: 'ON_WHATSAPP' });
    expect(await repo.listByStatus('PENDING_CHECK', 10)).toHaveLength(2);
    expect(await repo.listByStatus('PENDING_CHECK', 1)).toHaveLength(1);
  });

  it('saves a row with null e164 (invalid number)', async () => {
    await repo.save({ id: 'x', raw: 'junk', e164: null, status: 'INVALID', jid: null });
    expect(await repo.listByStatus('INVALID', 10)).toHaveLength(1);
  });
});
