import type { Proxy } from '../domain/account.js';
import type { CreateInstancePayload, ConnectResponse, SendTextResponse, CheckNumbersResponse } from './evolution-types.js';

export type FetchFn = typeof fetch;

export class EvolutionApiError extends Error {
  constructor(public status: number, message: string) {
    super(`Evolution API ${status}: ${message}`);
    this.name = 'EvolutionApiError';
  }
}

export class EvolutionClient {
  private baseUrl: string;
  private apiKey: string;
  private fetchFn: FetchFn;

  constructor(opts: { baseUrl: string; apiKey: string; fetchFn?: FetchFn }) {
    this.baseUrl = opts.baseUrl.replace(/\/$/, '');
    this.apiKey = opts.apiKey;
    this.fetchFn = opts.fetchFn ?? fetch;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.fetchFn(`${this.baseUrl}${path}`, {
      method,
      headers: { 'Content-Type': 'application/json', apikey: this.apiKey },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new EvolutionApiError(res.status, text);
    }
    return (await res.json()) as T;
  }

  async createInstance(p: {
    instanceName: string; number: string; proxy: Proxy; webhookUrl: string;
  }): Promise<void> {
    const payload: CreateInstancePayload = {
      instanceName: p.instanceName,
      number: p.number,
      integration: 'WHATSAPP-BAILEYS',
      proxyHost: p.proxy.host,
      proxyPort: String(p.proxy.port),
      proxyProtocol: p.proxy.protocol,
      proxyUsername: p.proxy.username,
      proxyPassword: p.proxy.password,
      webhook: {
        url: p.webhookUrl,
        events: ['CONNECTION_UPDATE', 'MESSAGES_UPDATE'],
      },
    };
    await this.request('POST', '/instance/create', payload);
  }

  async connect(instanceName: string): Promise<{ pairingCode: string | null }> {
    const r = await this.request<ConnectResponse>('GET', `/instance/connect/${instanceName}`);
    return { pairingCode: r.pairingCode ?? null };
  }

  async sendText(p: { instanceName: string; to: string; text: string }): Promise<{ providerMessageId: string }> {
    const r = await this.request<SendTextResponse>('POST', `/message/sendText/${p.instanceName}`, {
      number: p.to,
      text: p.text,
    });
    const id = r.key?.id;
    if (!id) throw new EvolutionApiError(200, 'sendText response missing key.id');
    return { providerMessageId: id };
  }

  async checkNumbers(p: { instanceName: string; numbers: string[] }): Promise<Array<{ number: string; exists: boolean; jid: string | null }>> {
    const r = await this.request<CheckNumbersResponse>('POST', `/chat/whatsappNumbers/${p.instanceName}`, {
      numbers: p.numbers,
    });
    return r.map((e) => ({
      number: e.number ?? '',
      exists: e.exists === true,
      jid: e.jid ?? null,
    }));
  }
}
