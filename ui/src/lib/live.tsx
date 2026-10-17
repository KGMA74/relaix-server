import { useQueryClient, type QueryKey } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { API_BASE, currentApiKey, notifyUnauthorized } from "../api/client";

/** One change announced by GET /events — see events.Event on the server. */
export interface LiveEvent {
  type: string;
  deviceId?: string;
  jobId?: string;
  status?: string;
  reenrolled?: boolean;
  at: string;
}

export type LiveStatus = "connecting" | "live" | "offline";

interface Live {
  status: LiveStatus;
  /** Calls fn for every event until the returned function is called. */
  subscribe: (fn: (e: LiveEvent) => void) => () => void;
}

const LiveContext = createContext<Live | null>(null);

// Reconnect delays: quick at first, then backing off so a gateway that is
// down for a while is not hammered by every open tab.
const RETRY_MS = [1000, 2000, 5000, 10_000, 30_000];

// Events arrive in bursts — a fanout of a hundred jobs, a fleet heartbeating.
// Invalidations are gathered for this long and applied once, so a burst costs
// one refetch per query rather than one per event.
const BATCH_MS = 250;

/**
 * Keeps one connection to GET /events and turns its events into query
 * invalidations: the events say what changed, the queries re-read it. Polling
 * stays on as a slow safety net (see usePollInterval) but is no longer what
 * makes the dashboard current.
 *
 * Read with fetch rather than EventSource, which cannot send the
 * Authorization header the API requires.
 */
export function LiveProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<LiveStatus>("connecting");
  const listeners = useRef(new Set<(e: LiveEvent) => void>());

  useEffect(() => {
    const abort = new AbortController();
    let attempt = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const pending = new Map<string, QueryKey>();
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    const invalidate = (...keys: QueryKey[]) => {
      for (const k of keys) pending.set(JSON.stringify(k), k);
      flushTimer ??= setTimeout(() => {
        flushTimer = undefined;
        for (const k of pending.values()) queryClient.invalidateQueries({ queryKey: k });
        pending.clear();
      }, BATCH_MS);
    };

    const dispatch = (name: string, data: string) => {
      if (name === "ready") {
        // The subscription exists from here: re-read everything once, so
        // whatever changed while disconnected is picked up.
        attempt = 0;
        setStatus("live");
        queryClient.invalidateQueries();
        return;
      }
      let e: LiveEvent;
      try {
        e = JSON.parse(data) as LiveEvent;
      } catch {
        return;
      }
      if (e.type.startsWith("device.")) invalidate(["devices"], ["stats"]);
      if (e.type === "sms.received") invalidate(["inbox"]);
      if (e.type === "automation.run") invalidate(["runs"]);
      if (e.type.startsWith("job.")) {
        invalidate(["jobs"], ["stats"]);
        if (e.jobId) invalidate(["job", e.jobId]);
      }
      for (const fn of listeners.current) fn(e);
    };

    const connect = async () => {
      const key = currentApiKey();
      setStatus((s) => (s === "live" ? "connecting" : s));
      try {
        const res = await fetch(API_BASE + "/events", {
          // Without a key, the browser's own credentials (a session cookie) go instead.
          headers: key ? { Authorization: `Bearer ${key}`, Accept: "text/event-stream" } : { Accept: "text/event-stream" },
          signal: abort.signal,
        });
        if (res.status === 401) {
          notifyUnauthorized();
          return;
        }
        if (!res.ok || !res.body) throw new Error(`events: HTTP ${res.status}`);

        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += value;
          // Frames end with a blank line. Anything after the last one is a
          // frame still arriving.
          let end: number;
          while ((end = buffer.indexOf("\n\n")) !== -1) {
            const frame = buffer.slice(0, end);
            buffer = buffer.slice(end + 2);
            let name = "message";
            const data: string[] = [];
            for (const line of frame.split("\n")) {
              if (line.startsWith("event: ")) name = line.slice(7);
              else if (line.startsWith("data: ")) data.push(line.slice(6));
            }
            if (data.length) dispatch(name, data.join("\n"));
          }
        }
      } catch {
        if (abort.signal.aborted) return;
      }
      // The stream ended: server restart, dropped for falling behind, network.
      setStatus("offline");
      const delay = RETRY_MS[Math.min(attempt++, RETRY_MS.length - 1)];
      retryTimer = setTimeout(connect, delay);
    };

    void connect();
    return () => {
      abort.abort();
      clearTimeout(retryTimer);
      clearTimeout(flushTimer);
    };
  }, [queryClient]);

  const value = useRef<Live>({
    status,
    subscribe: (fn) => {
      listeners.current.add(fn);
      return () => listeners.current.delete(fn);
    },
  });
  value.current = { ...value.current, status };

  return <LiveContext.Provider value={value.current}>{children}</LiveContext.Provider>;
}

export function useLive(): Live {
  const ctx = useContext(LiveContext);
  if (!ctx) throw new Error("useLive outside LiveProvider");
  return ctx;
}

/** Runs fn for every live event while the component is mounted. */
export function useLiveEvent(fn: (e: LiveEvent) => void) {
  const { subscribe } = useLive();
  const latest = useRef(fn);
  latest.current = fn;
  useEffect(() => subscribe((e) => latest.current(e)), [subscribe]);
}

/**
 * How often a query should poll. Live, only as a safety net for a missed
 * event; otherwise often enough that the screen stays honest.
 */
export function usePollInterval(fallbackMs: number): number {
  const { status } = useLive();
  return status === "live" ? 60_000 : fallbackMs;
}
