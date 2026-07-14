import { Prisma } from '@prisma/client';
import type { PrismaClient } from '@prisma/client';
import type { Account, Proxy } from '../domain/account.js';
import type { AccountState } from '../domain/account-state.js';
import type { AccountRepository } from '../ports/account-repository.js';

interface Row {
  id: string; phoneNumber: string; state: string; instanceName: string;
  proxyJson: unknown; deadReason: string | null;
}

function toAccount(r: Row): Account {
  return {
    id: r.id,
    phoneNumber: r.phoneNumber,
    state: r.state as AccountState,
    instanceName: r.instanceName,
    proxy: (r.proxyJson as Proxy | null) ?? null,
    deadReason: r.deadReason,
  };
}

export class PrismaAccountRepository implements AccountRepository {
  constructor(private prisma: PrismaClient) {}

  async save(a: Account): Promise<void> {
    const data = {
      phoneNumber: a.phoneNumber,
      state: a.state,
      instanceName: a.instanceName,
      proxyJson: (a.proxy as Prisma.InputJsonValue | null) ?? undefined,
      deadReason: a.deadReason,
    };
    await this.prisma.account.upsert({
      where: { id: a.id },
      create: { id: a.id, ...data },
      update: { ...data, proxyJson: (a.proxy as Prisma.InputJsonValue | null) ?? Prisma.DbNull },
    });
  }

  async findById(id: string): Promise<Account | null> {
    const r = await this.prisma.account.findUnique({ where: { id } });
    return r ? toAccount(r as unknown as Row) : null;
  }

  async findByInstanceName(name: string): Promise<Account | null> {
    const r = await this.prisma.account.findUnique({ where: { instanceName: name } });
    return r ? toAccount(r as unknown as Row) : null;
  }
}
