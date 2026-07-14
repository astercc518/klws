import { describe, it, expect } from 'vitest';
import { loadConfig } from '../src/config.js';

describe('loadConfig', () => {
  it('reads all fields from env', () => {
    const cfg = loadConfig({
      EVOLUTION_BASE_URL: 'http://x:8080',
      EVOLUTION_API_KEY: 'k',
      DATABASE_URL: 'postgresql://a',
      WEBHOOK_PORT: '3000',
      WEBHOOK_SECRET: 's3cr3t',
    });
    expect(cfg).toEqual({
      evolutionBaseUrl: 'http://x:8080',
      evolutionApiKey: 'k',
      databaseUrl: 'postgresql://a',
      webhookPort: 3000,
      webhookSecret: 's3cr3t',
    });
  });

  it('throws when a required var is missing', () => {
    expect(() => loadConfig({})).toThrow(/EVOLUTION_BASE_URL/);
  });

  it('throws when WEBHOOK_SECRET is missing', () => {
    expect(() =>
      loadConfig({
        EVOLUTION_BASE_URL: 'http://x:8080',
        EVOLUTION_API_KEY: 'k',
        DATABASE_URL: 'postgresql://a',
        WEBHOOK_PORT: '3000',
      }),
    ).toThrow(/WEBHOOK_SECRET/);
  });
});
