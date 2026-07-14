import type { Message, MessageStatus } from '../domain/message.js';
import type { MessageRepository } from '../ports/message-repository.js';

export class InMemoryMessageRepository implements MessageRepository {
  private byId = new Map<string, Message>();

  async save(m: Message): Promise<void> {
    this.byId.set(m.id, { ...m });
  }
  async findByProviderMessageId(pid: string): Promise<Message | null> {
    for (const m of this.byId.values()) if (m.providerMessageId === pid) return { ...m };
    return null;
  }
  async updateStatus(id: string, status: MessageStatus): Promise<void> {
    const m = this.byId.get(id);
    if (m) this.byId.set(id, { ...m, status });
  }
}
