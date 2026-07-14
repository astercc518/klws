export interface CreateInstancePayload {
  instanceName: string;
  number: string;
  integration: 'WHATSAPP-BAILEYS';
  proxyHost: string;
  proxyPort: string;
  proxyProtocol: 'http' | 'socks5';
  proxyUsername?: string;
  proxyPassword?: string;
  webhook: { url: string; events: string[] };
}

export interface ConnectResponse { pairingCode?: string | null; code?: string }
export interface SendTextResponse { key?: { id?: string } }

export interface CheckNumberEntry { number?: string; exists?: boolean; jid?: string | null }
export type CheckNumbersResponse = CheckNumberEntry[];
