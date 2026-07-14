import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupStage } from '../warmup/warmup-state.js';

export interface WarmupProfileRepository {
  save(p: WarmupProfile): Promise<void>;
  findByAccountId(accountId: string): Promise<WarmupProfile | null>;
  listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]>;
}
