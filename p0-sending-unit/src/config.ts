export interface Config {
  evolutionBaseUrl: string;
  evolutionApiKey: string;
  databaseUrl: string;
  webhookPort: number;
}

function required(env: NodeJS.ProcessEnv, key: string): string {
  const v = env[key];
  if (!v) throw new Error(`Missing required env var: ${key}`);
  return v;
}

export function loadConfig(env: NodeJS.ProcessEnv): Config {
  return {
    evolutionBaseUrl: required(env, 'EVOLUTION_BASE_URL'),
    evolutionApiKey: required(env, 'EVOLUTION_API_KEY'),
    databaseUrl: required(env, 'DATABASE_URL'),
    webhookPort: Number(required(env, 'WEBHOOK_PORT')),
  };
}
