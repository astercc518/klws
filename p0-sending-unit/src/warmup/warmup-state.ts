export type WarmupStage = 'NEW' | 'WARMING' | 'MATURE';
export type WarmupEvent = { type: 'ENROLL' } | { type: 'PROMOTE' };

export class WarmupTransitionError extends Error {
  constructor(stage: WarmupStage, event: WarmupEvent['type']) {
    super(`Invalid warmup transition: ${event} from ${stage}`);
    this.name = 'WarmupTransitionError';
  }
}

const TABLE: Record<WarmupStage, Partial<Record<WarmupEvent['type'], WarmupStage>>> = {
  NEW: { ENROLL: 'WARMING' },
  WARMING: { PROMOTE: 'MATURE' },
  MATURE: {},
};

export function warmupTransition(stage: WarmupStage, event: WarmupEvent): WarmupStage {
  const next = TABLE[stage][event.type];
  if (!next) throw new WarmupTransitionError(stage, event.type);
  return next;
}
