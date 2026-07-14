import type { TargetNumber, TargetStatus } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';

export class InMemoryTargetNumberRepository implements TargetNumberRepository {
  private byId = new Map<string, TargetNumber>();

  async save(t: TargetNumber): Promise<void> {
    this.byId.set(t.id, { ...t });
  }

  async findByE164(e164: string): Promise<TargetNumber | null> {
    for (const t of this.byId.values()) {
      if (t.e164 !== null && t.e164 === e164) return { ...t };
    }
    return null;
  }

  async listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]> {
    const out: TargetNumber[] = [];
    for (const t of this.byId.values()) {
      if (t.status === status) out.push({ ...t });
      if (out.length >= limit) break;
    }
    return out;
  }
}
