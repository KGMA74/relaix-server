import type { Activity, AutomationRun, AutomationTemplate, Country, Device, Policy, EnrollToken, InboundMessage, InboxPage, Job, JobEvent, JobKind, JobPage, SendRequest, Stats, UssdRequest } from "./types";

// The API key lives in sessionStorage: it survives a reload but not closing the
// tab, which is the right lifetime for an admin credential typed by hand.
const KEY = "relaix.apiKey";
let memoryKey: string | null = null;

export function getApiKey(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function setApiKey(key: string | null) {
  try {
    if (key === null) sessionStorage.removeItem(KEY);
    else sessionStorage.setItem(KEY, key);
  } catch {
    // Storage blocked: the key then lasts as long as the page, via memory.
  }
  memoryKey = key;
}

/** ApiError carries the gateway's machine-readable code beside its message. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/** The key requests are sent with: stored, or kept in memory when storage is blocked. */
export function currentApiKey(): string | null {
  return getApiKey() ?? memoryKey;
}

// Headers added to every request by an app embedding these pages — a CSRF
// token for a cookie session, say. None by default.
let extraHeaders: (method: string) => Record<string, string> = () => ({});

export function setExtraHeaders(fn: (method: string) => Record<string, string>) {
  extraHeaders = fn;
}

// Called on any 401 so the app can drop back to the login screen.
let onUnauthorized: () => void = () => {};

/** For callers that talk to the API outside request(), like the event stream. */
export function notifyUnauthorized() {
  onUnauthorized();
}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

/** Where the API is published (api.APIPrefix on the server). */
export const API_BASE = "/api/v1";

/** A call to the gateway, for routes the api object below does not cover. */
export async function request<T>(method: string, path: string, body?: unknown, key?: string): Promise<T> {
  const apiKey = key ?? getApiKey() ?? memoryKey;
  const headers: Record<string, string> = { ...extraHeaders(method) };
  if (apiKey) headers.Authorization = `Bearer ${apiKey}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(API_BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;

  if (!res.ok) {
    const err = data?.error ?? {};
    if (res.status === 401 && key === undefined) onUnauthorized();
    throw new ApiError(res.status, err.code ?? "http_" + res.status, err.message ?? res.statusText);
  }
  return data as T;
}

export interface JobQuery {
  kind?: JobKind;
  status?: string[];
  deviceId?: string;
  recipient?: string;
  from?: string;
  to?: string;
  limit?: number;
  cursor?: string;
}

function query(q: JobQuery): string {
  const p = new URLSearchParams();
  if (q.kind) p.set("kind", q.kind);
  if (q.status?.length) p.set("status", q.status.join(","));
  if (q.deviceId) p.set("deviceId", q.deviceId);
  if (q.recipient) p.set("recipient", q.recipient);
  if (q.from) p.set("from", q.from);
  if (q.to) p.set("to", q.to);
  if (q.limit) p.set("limit", String(q.limit));
  if (q.cursor) p.set("cursor", q.cursor);
  const s = p.toString();
  return s ? "?" + s : "";
}

export interface InboxQuery {
  deviceId?: string;
  sender?: string;
  limit?: number;
  cursor?: string;
}

function inboxQuery(q: InboxQuery): string {
  const p = new URLSearchParams();
  if (q.deviceId) p.set("deviceId", q.deviceId);
  if (q.sender) p.set("sender", q.sender);
  if (q.limit) p.set("limit", String(q.limit));
  if (q.cursor) p.set("cursor", q.cursor);
  const s = p.toString();
  return s ? "?" + s : "";
}

export const api = {
  /** Checks a key without touching the stored one. */
  verifyKey: (key: string) => request<Stats>("GET", "/stats", undefined, key),

  activity: (window = "24h") => request<Activity>("GET", `/stats/activity?window=${encodeURIComponent(window)}`),

  stats: (window = "24h") => request<Stats>("GET", `/stats?window=${encodeURIComponent(window)}`),

  listJobs: (q: JobQuery) => request<JobPage>("GET", "/jobs" + query(q)),
  getJob: (id: string) => request<Job>("GET", `/jobs/${id}`),
  jobEvents: (id: string) => request<{ events: JobEvent[] }>("GET", `/jobs/${id}/events`),
  cancelJob: (id: string) => request<Job>("DELETE", `/jobs/${id}`),

  /** POST /send answers one job for `recipient`, a list for `recipients`. */
  send: (req: SendRequest) => request<Job | { jobs: Job[] }>("POST", "/send", req),

  /** Runs a USSD code on one phone; the answer arrives on the job. */
  ussd: (req: UssdRequest) => request<Job>("POST", "/ussd", req),

  listInbox: (q: InboxQuery) => request<InboxPage>("GET", "/inbox" + inboxQuery(q)),
  getInbound: (id: string) => request<InboundMessage>("GET", `/inbox/${id}`),

  library: () => request<{ templates: AutomationTemplate[] }>("GET", "/automations/library"),
  listRuns: (deviceId?: string) =>
    request<{ runs: AutomationRun[] }>("GET", "/automations/runs" + (deviceId ? `?deviceId=${deviceId}` : "")),

  listDevices: () => request<{ devices: Device[] }>("GET", "/devices"),
  setDeviceEnabled: (id: string, enabled: boolean) => request<Device>("PATCH", `/devices/${id}`, { enabled }),
  deleteDevice: (id: string) => request<void>("DELETE", `/devices/${id}`),
  putPolicy: (id: string, policy: Omit<Policy, "version" | "updatedAt">) =>
    request<Policy>("PUT", `/devices/${id}/policy`, policy),
  countries: () => request<{ countries: Country[] }>("GET", "/countries"),
  enrollToken: () => request<EnrollToken>("POST", "/admin/devices/enroll-token"),
};
