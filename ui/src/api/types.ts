// Mirrors of the gatewayd REST responses (server/api). Kept by hand: the API
// is small, and a generated client would be a second build step for a handful
// of types.

export type JobStatus = "pending" | "assigned" | "sent" | "delivered" | "failed" | "cancelled";

export const JOB_STATUSES: JobStatus[] = ["pending", "assigned", "sent", "delivered", "failed", "cancelled"];

export const TERMINAL_STATUSES: JobStatus[] = ["sent", "delivered", "failed", "cancelled"];

export type JobMode = "immediate" | "queued";

export type JobKind = "sms" | "ussd";

export interface Job {
  jobId: string;
  /** "sms" sends body to recipient; "ussd" dials body (a code) on deviceId. */
  kind: JobKind;
  status: JobStatus;
  recipient: string;
  body: string;
  mode: JobMode;
  priority: number;
  deviceId?: string;
  subscriptionId?: number;
  /** The SIM the job was handed to: requested, or picked by the device's rules. */
  sentFromSubscriptionId?: number;
  attempts: number;
  partsSent: number;
  errorCode?: string;
  errorMessage?: string;
  scheduledAt?: string;
  expiresAt?: string;
  createdAt: string;
  completedAt?: string;
  /** USSD: answers typed into the menus (hidden when sensitive). */
  replies?: string[];
  /** USSD: what the network displayed, one entry per screen. */
  responses?: string[];
  /** USSD: the code and replies carry a secret and are never shown. */
  sensitive?: boolean;
}

export interface JobPage {
  jobs: Job[];
  nextCursor?: string;
}

export interface JobEvent {
  status?: JobStatus;
  deviceId?: string;
  reason?: string;
  createdAt: string;
}

export interface SendRequest {
  recipient?: string;
  recipients?: string[];
  body: string;
  deviceId?: string;
  subscriptionId?: number;
  mode?: JobMode;
  priority?: number;
  scheduledAt?: string;
  expiresAt?: string;
  callbackUrl?: string;
}

export interface UssdRequest {
  deviceId: string;
  code: string;
  subscriptionId?: number;
  replies?: string[];
  sensitive?: boolean;
  mode?: JobMode;
  expiresAt?: string;
  callbackUrl?: string;
}

/** An SMS a phone received and its rules forwarded. */
export interface InboundMessage {
  messageId: string;
  deviceId?: string;
  subscriptionId?: number;
  sender: string;
  body: string;
  receivedAt: string;
  createdAt: string;
  webhook?: { delivered: boolean; attempts: number; lastError?: string };
}

export interface InboxPage {
  messages: InboundMessage[];
  nextCursor?: string;
}

export interface Health {
  batteryLevel: number;
  isCharging: boolean;
  signalStrength: number;
  networkType?: string;
  simReady: boolean;
  sentLastHour: number;
  permissionsOk: boolean;
  reportedAt: string;
}

export interface Sim {
  subscriptionId: number;
  slotIndex: number;
  phoneNumber?: string;
  carrier?: string;
  displayName?: string;
  isDefaultSms: boolean;
  signalStrength: number;
  simReady: boolean;
  sentLastHour: number;
}

export interface Device {
  deviceId: string;
  label: string;
  phoneNumber?: string;
  enabled: boolean;
  connected: boolean;
  ready: boolean;
  model?: string;
  osVersion?: string;
  carrier?: string;
  sims?: Sim[];
  health?: Health;
  lastSeenAt?: string;
  createdAt: string;
  /** IANA zone the phone reports; the policy's windows default to it. */
  timeZone?: string;
  policy: Policy;
  /** The device's own hours are open now (SIM hours aside). */
  availableNow: boolean;
  /** What the device was handed, as the scheduler counts it against its caps. */
  usage: Usage;
  /** What the agent can do beyond SMS: "ussd", "ussd_menu", "inbound_sms". */
  capabilities: string[];
}

export interface UsageCount {
  lastHour: number;
  lastDay: number;
}

export interface Usage extends UsageCount {
  /** Per subscription id, for SIMs named on their jobs. */
  sims?: Record<string, UsageCount>;
}

/** Caps over sliding windows, in messages. 0 or absent: no cap. */
export interface SendLimits {
  perHour?: number;
  perDay?: number;
}

/** A daily slot. Minutes since midnight; end exclusive; end <= start runs past midnight. */
export interface TimeWindow {
  /** ISO weekdays, 1 = Monday … 7 = Sunday. Empty: every day. */
  days?: number[];
  start: number;
  end: number;
}

export interface Availability {
  timeZone?: string;
  /** None: always available. */
  windows?: TimeWindow[];
}

export interface AcceptRules {
  /** ISO 3166-1 alpha-2. Empty: any country. */
  countries?: string[];
  /** RE2 expression on the E.164 recipient. Empty: any number. */
  pattern?: string;
}

export interface SimPolicy {
  subscriptionId: number;
  disabled?: boolean;
  accept: AcceptRules;
  availability: Availability;
  limits?: SendLimits;
}

/** When a device takes work and for whom. Every empty field restricts nothing. */
export interface Policy {
  version: number;
  availability: Availability;
  accept: AcceptRules;
  sims?: SimPolicy[];
  updatedAt?: string;
  limits?: SendLimits;
  /** Which received SMS the phone forwards. Off unless enabled. */
  inbound?: InboundRules;
}

export interface InboundRules {
  enabled?: boolean;
  /** Matched whole, ignoring case. Empty, when enabled: every sender. */
  senders?: string[];
}

export interface Country {
  code: string;
  prefixes: string[];
}

export interface Stats {
  since: string;
  jobs: Record<JobStatus, number>;
  total: number;
  devices: { total: number; enabled: number; connected: number; ready: number };
}

export interface EnrollToken {
  token: string;
  expiresAt: string;
  payload: string;
  qrCodePngBase64: string;
}

export interface ActivityBucket {
  start: string;
  /** Sent and delivered: the network took it. */
  delivered: number;
  failed: number;
  /** Pending and assigned. */
  inFlight: number;
  cancelled: number;
}

export interface ActivityTotals {
  total: number;
  delivered: number;
  failed: number;
}

export interface Activity {
  window: string;
  bucketSeconds: number;
  buckets: ActivityBucket[];
  current: ActivityTotals;
  previous: ActivityTotals;
}
