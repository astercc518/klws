import { normalizeBrPhone } from './br-phone.js';
import type { TargetNumber } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';

export interface IdGen { next(): string }

export class ScreeningService {
  constructor(
    private deps: {
      targets: TargetNumberRepository;
      evolution: Pick<EvolutionClient, 'checkNumbers'>;
      ids: IdGen;
    },
  ) {}

  async importNumbers(raws: string[]): Promise<{ imported: number; invalid: number; duplicates: number }> {
    let imported = 0;
    let invalid = 0;
    let duplicates = 0;

    for (const raw of raws) {
      const norm = normalizeBrPhone(raw);
      if (!norm.ok) {
        await this.deps.targets.save({
          id: this.deps.ids.next(), raw, e164: null, status: 'INVALID', jid: null,
        });
        invalid += 1;
        continue;
      }
      const existing = await this.deps.targets.findByE164(norm.e164);
      if (existing) {
        duplicates += 1;
        continue;
      }
      await this.deps.targets.save({
        id: this.deps.ids.next(), raw, e164: norm.e164, status: 'PENDING_CHECK', jid: null,
      });
      imported += 1;
    }

    return { imported, invalid, duplicates };
  }

  async runScreening(checkerInstanceName: string, batchSize: number): Promise<{ checked: number; onWhatsApp: number; notOnWhatsApp: number }> {
    const pending = await this.deps.targets.listByStatus('PENDING_CHECK', batchSize);
    if (pending.length === 0) {
      return { checked: 0, onWhatsApp: 0, notOnWhatsApp: 0 };
    }

    const numbers = pending.map((p) => p.e164).filter((e): e is string => e !== null);
    const results = await this.deps.evolution.checkNumbers({ instanceName: checkerInstanceName, numbers });
    const byNumber = new Map(results.map((r) => [r.number, r]));

    let onWhatsApp = 0;
    let notOnWhatsApp = 0;
    let checked = 0;

    for (const target of pending) {
      if (target.e164 === null) continue;
      const result = byNumber.get(target.e164);
      if (!result) continue;
      checked += 1;
      const updated: TargetNumber = result.exists
        ? { ...target, status: 'ON_WHATSAPP', jid: result.jid }
        : { ...target, status: 'NOT_ON_WHATSAPP', jid: null };
      await this.deps.targets.save(updated);
      if (result.exists) onWhatsApp += 1; else notOnWhatsApp += 1;
    }

    return { checked, onWhatsApp, notOnWhatsApp };
  }
}
