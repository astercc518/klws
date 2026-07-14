import type { PrismaClient } from '@prisma/client';
import type { TargetNumber, TargetStatus } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';

interface Row { id: string; raw: string; e164: string | null; status: string; jid: string | null }

function toTarget(r: Row): TargetNumber {
  return { id: r.id, raw: r.raw, e164: r.e164, status: r.status as TargetStatus, jid: r.jid };
}

export class PrismaTargetNumberRepository implements TargetNumberRepository {
  constructor(private prisma: PrismaClient) {}

  async save(t: TargetNumber): Promise<void> {
    const data = { raw: t.raw, e164: t.e164, status: t.status, jid: t.jid };
    await this.prisma.targetNumber.upsert({
      where: { id: t.id },
      create: { id: t.id, ...data },
      update: data,
    });
  }

  async findByE164(e164: string): Promise<TargetNumber | null> {
    const r = await this.prisma.targetNumber.findUnique({ where: { e164 } });
    return r ? toTarget(r as unknown as Row) : null;
  }

  async listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]> {
    const rows = await this.prisma.targetNumber.findMany({ where: { status }, take: limit });
    return rows.map((r) => toTarget(r as unknown as Row));
  }
}
