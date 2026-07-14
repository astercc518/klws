import { describe, it, expect } from 'vitest';
import { warmupTransition, WarmupTransitionError } from '../../src/warmup/warmup-state.js';

describe('warmup state machine', () => {
  it('NEW +ENROLL -> WARMING', () => {
    expect(warmupTransition('NEW', { type: 'ENROLL' })).toBe('WARMING');
  });
  it('WARMING +PROMOTE -> MATURE', () => {
    expect(warmupTransition('WARMING', { type: 'PROMOTE' })).toBe('MATURE');
  });
  it('rejects illegal transitions', () => {
    expect(() => warmupTransition('NEW', { type: 'PROMOTE' })).toThrow(WarmupTransitionError);
    expect(() => warmupTransition('MATURE', { type: 'ENROLL' })).toThrow(WarmupTransitionError);
    expect(() => warmupTransition('WARMING', { type: 'ENROLL' })).toThrow(WarmupTransitionError);
  });
});
