// lib/api.ts — thin fetch wrapper around the wadist Go/Gin JSON API.
//
// Contract with the backend (internal/api): every response is the envelope
//   { "code": <int>, "data": <T>, "message": <string> }
// and authentication is a Bearer token (the HMAC-signed session id returned by
// POST /api/v1/auth/login). This module:
//   1. auto-attaches the token from localStorage on every request,
//   2. globally intercepts 401 → clears the token and redirects to /login,
//   3. unwraps the envelope and returns the typed `data`, throwing ApiError
//      on any non-success so callers can `try/catch`.

const API_BASE_URL =
  process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080/api/v1";

const TOKEN_KEY = "wadist_token";
const LOGIN_PATH = "/login";

// ---------------------------------------------------------------------------
// Token store (localStorage, SSR-safe).
// ---------------------------------------------------------------------------

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(TOKEN_KEY);
}

// ---------------------------------------------------------------------------
// Error type + envelope shape.
// ---------------------------------------------------------------------------

/** ApiError carries the HTTP status and the backend's `message`. */
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

interface Envelope<T> {
  code: number;
  data: T;
  message: string;
}

// ---------------------------------------------------------------------------
// Core request.
// ---------------------------------------------------------------------------

type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown };

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { body, headers, ...rest } = options;

  const finalHeaders: Record<string, string> = {
    "Content-Type": "application/json",
    ...(headers as Record<string, string> | undefined),
  };

  const token = getToken();
  if (token) finalHeaders["Authorization"] = `Bearer ${token}`;

  const url = path.startsWith("http")
    ? path
    : `${API_BASE_URL}${path.startsWith("/") ? path : `/${path}`}`;

  let res: Response;
  try {
    res = await fetch(url, {
      ...rest,
      headers: finalHeaders,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, "network error: could not reach API");
  }

  // --- global 401 interception: not logged in / session expired ---
  if (res.status === 401) {
    clearToken();
    if (typeof window !== "undefined" && window.location.pathname !== LOGIN_PATH) {
      window.location.href = LOGIN_PATH;
    }
    throw new ApiError(401, "unauthorized");
  }

  // 204 / empty body: nothing to unwrap.
  const text = await res.text();
  if (!text) {
    if (!res.ok) throw new ApiError(res.status, res.statusText);
    return undefined as T;
  }

  let env: Envelope<T>;
  try {
    env = JSON.parse(text) as Envelope<T>;
  } catch {
    throw new ApiError(res.status, "invalid JSON response from API");
  }

  // The backend uses the envelope `code` as the source of truth, mirrored to
  // the HTTP status. Treat anything other than 200 as an error.
  if (!res.ok || env.code !== 200) {
    throw new ApiError(res.status, env.message || res.statusText);
  }

  return env.data;
}

// ---------------------------------------------------------------------------
// Public verb helpers.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// File download (CSV export). Bearer-authenticated GET → blob → browser save.
// Kept separate from `request` because it does not unwrap the JSON envelope.
// ---------------------------------------------------------------------------

export async function download(path: string, filename: string): Promise<void> {
  const url = path.startsWith("http")
    ? path
    : `${API_BASE_URL}${path.startsWith("/") ? path : `/${path}`}`;
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(url, { method: "GET", headers });
  } catch {
    throw new ApiError(0, "network error: could not reach API");
  }
  if (res.status === 401) {
    clearToken();
    if (typeof window !== "undefined" && window.location.pathname !== LOGIN_PATH) {
      window.location.href = LOGIN_PATH;
    }
    throw new ApiError(401, "unauthorized");
  }
  if (!res.ok) throw new ApiError(res.status, res.statusText);

  const blob = await res.blob();
  const objectUrl = window.URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = objectUrl;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(objectUrl);
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "GET" }),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "POST", body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "PUT", body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "PATCH", body }),
  delete: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "DELETE" }),
  download,
};

// ---------------------------------------------------------------------------
// Auth convenience wrappers (mirror internal/api/auth.go).
// ---------------------------------------------------------------------------

export interface LoginResponse {
  token: string;
  role: "admin" | "sales" | "customer";
  tenant_id: number | null;
}

/** login posts credentials, persists the returned Bearer token, returns it.
 *  `turnstileToken` (Cloudflare) is forwarded for server-side verification. */
export async function login(
  email: string,
  password: string,
  turnstileToken?: string,
): Promise<LoginResponse> {
  const data = await api.post<LoginResponse>("/auth/login", {
    email,
    password,
    cf_turnstile_token: turnstileToken,
  });
  setToken(data.token);
  return data;
}

export interface RegisterPayload {
  email: string;
  password: string;
  turnstileToken?: string;
}

/** register creates an account; on success the backend returns a session token. */
export async function register(payload: RegisterPayload): Promise<LoginResponse> {
  const data = await api.post<LoginResponse>("/auth/register", {
    email: payload.email,
    password: payload.password,
    cf_turnstile_token: payload.turnstileToken,
  });
  setToken(data.token);
  return data;
}

/** requestPasswordReset asks the backend to email a reset link (best-effort). */
export async function requestPasswordReset(
  email: string,
  turnstileToken?: string,
): Promise<void> {
  await api.post<void>("/auth/password/forgot", {
    email,
    cf_turnstile_token: turnstileToken,
  });
}

/** resetPassword consumes a reset token and sets a new password. */
export async function resetPassword(token: string, password: string): Promise<void> {
  await api.post<void>("/auth/password/reset", { token, password });
}

/** logout revokes the server session (best-effort) and clears the local token. */
export async function logout(): Promise<void> {
  try {
    await api.post<void>("/auth/logout");
  } finally {
    clearToken();
  }
}
