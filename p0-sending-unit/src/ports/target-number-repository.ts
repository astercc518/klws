import type { TargetNumber, TargetStatus } from '../domain/target-number.js';

export interface TargetNumberRepository {
  save(t: TargetNumber): Promise<void>;
  findByE164(e164: string): Promise<TargetNumber | null>;
  listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]>;
}
