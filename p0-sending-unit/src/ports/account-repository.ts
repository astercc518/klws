import type { Account } from '../domain/account.js';

export interface AccountRepository {
  save(a: Account): Promise<void>;
  findById(id: string): Promise<Account | null>;
  findByInstanceName(name: string): Promise<Account | null>;
}
