import type { Message, MessageStatus } from '../domain/message.js';

export interface MessageRepository {
  save(m: Message): Promise<void>;
  findByProviderMessageId(pid: string): Promise<Message | null>;
  updateStatus(id: string, status: MessageStatus): Promise<void>;
}
