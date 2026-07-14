import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';
import type { WarmupStage } from '../warmup/warmup-state.js';

export class InMemoryWarmupProfileRepository implements WarmupProfileRepository {
  private byAccount = new Map<string, WarmupProfile>();

  async save(p: WarmupProfile): Promise<void> {
    this.byAccount.set(p.accountId, { ...p });
  }
  async findByAccountId(accountId: string): Promise<WarmupProfile | null> {
    const p = this.byAccount.get(accountId);
    return p ? { ...p } : null;
  }
  async listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]> {
    const out: WarmupProfile[] = [];
    for (const p of this.byAccount.values()) {
      if (p.stage === stage) out.push({ ...p });
      if (out.length >= limit) break;
    }
    return out;
  }
}
