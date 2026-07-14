export type TargetStatus = 'PENDING_CHECK' | 'ON_WHATSAPP' | 'NOT_ON_WHATSAPP' | 'INVALID';

export interface TargetNumber {
  id: string;
  raw: string;
  e164: string | null;
  status: TargetStatus;
  jid: string | null;
}
