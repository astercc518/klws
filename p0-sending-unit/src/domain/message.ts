export type MessageStatus = 'PENDING' | 'SENT' | 'DELIVERED' | 'READ' | 'FAILED';

export interface Message {
  id: string;
  accountId: string;
  to: string;
  providerMessageId: string | null;
  status: MessageStatus;
}
