import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

// Load .env into process.env so tests that construct PrismaClient find DATABASE_URL.
// Prisma Client (5.x) does not auto-load .env at runtime — only the Prisma CLI does.
const envPath = join(dirname(fileURLToPath(import.meta.url)), '.env');
try {
  for (const line of readFileSync(envPath, 'utf8').split('\n')) {
    const m = line.match(/^\s*([\w.]+)\s*=\s*(.*?)\s*$/);
    if (m && m[1] && process.env[m[1]] === undefined) {
      process.env[m[1]] = m[2] ?? '';
    }
  }
} catch {
  // .env is optional (pure-logic tests do not need it)
}
