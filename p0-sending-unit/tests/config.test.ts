import { describe, it, expect } from 'vitest';
import { loadConfig } from '../src/config.js';

describe('loadConfig', () => {
  it('reads all fields from env', () => {
    const cfg = loadConfig({
      EVOLUTION_BASE_URL: 'http://x:8080',
      EVOLUTION_API_KEY: 'k',
      DATABASE_URL: 'postgresql://a',
      WEBHOOK_PORT: '3000',
    });
    expect(cfg).toEqual({
      evolutionBaseUrl: 'http://x:8080',
      evolutionApiKey: 'k',
      databaseUrl: 'postgresql://a',
      webhookPort: 3000,
    });
  });

  it('throws when a required var is missing', () => {
    expect(() => loadConfig({})).toThrow(/EVOLUTION_BASE_URL/);
  });
});
