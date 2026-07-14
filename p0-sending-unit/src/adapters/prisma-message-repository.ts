import type { PrismaClient } from '@prisma/client';
import type { Message, MessageStatus } from '../domain/message.js';
import type { MessageRepository } from '../ports/message-repository.js';

export class PrismaMessageRepository implements MessageRepository {
  constructor(private prisma: PrismaClient) {}

  async save(m: Message): Promise<void> {
    await this.prisma.message.upsert({
      where: { id: m.id },
      create: { id: m.id, accountId: m.accountId, to: m.to, providerMessageId: m.providerMessageId, status: m.status },
      update: { status: m.status, providerMessageId: m.providerMessageId },
    });
  }

  async findByProviderMessageId(pid: string): Promise<Message | null> {
    const r = await this.prisma.message.findUnique({ where: { providerMessageId: pid } });
    return r
      ? { id: r.id, accountId: r.accountId, to: r.to, providerMessageId: r.providerMessageId, status: r.status as MessageStatus }
      : null;
  }

  async updateStatus(id: string, status: MessageStatus): Promise<void> {
    await this.prisma.message.update({ where: { id }, data: { status } });
  }
}
