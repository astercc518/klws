import type { Account } from '../domain/account.js';
import type { AccountRepository } from '../ports/account-repository.js';

export class InMemoryAccountRepository implements AccountRepository {
  private byId = new Map<string, Account>();

  async save(a: Account): Promise<void> {
    this.byId.set(a.id, { ...a });
  }
  async findById(id: string): Promise<Account | null> {
    const a = this.byId.get(id);
    return a ? { ...a } : null;
  }
  async findByInstanceName(name: string): Promise<Account | null> {
    for (const a of this.byId.values()) if (a.instanceName === name) return { ...a };
    return null;
  }
}
