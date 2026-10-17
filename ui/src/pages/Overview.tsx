import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDownRight,
  ArrowRight,
  ArrowUpRight,
  BatteryCharging,
  BatteryMedium,
  CheckCircle2,
  Inbox,
  Smartphone,
  XCircle,
} from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { JOB_STATUSES, type Device, type JobStatus } from "../api/types";
import { ActivityChart } from "../components/ActivityChart";
import { CardHeader, Empty, ErrorNote, PageHeader, SignalBars, STATUS_LABEL, STATUS_TONE, StatusBadge, type Tone } from "../components/common";
import { formatRelative } from "../lib/format";
import { usePollInterval } from "@/lib/live";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

const WINDOWS = [
  { value: "1h", label: "1 h", previous: "l'heure d'avant" },
  { value: "24h", label: "24 h", previous: "les 24 h d'avant" },
  { value: "168h", label: "7 j", previous: "la semaine d'avant" },
  { value: "720h", label: "30 j", previous: "les 30 j d'avant" },
];

const REFRESH_MS = 10_000;

const TONE_BG: Record<Tone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  info: "bg-info",
  primary: "bg-primary",
  neutral: "bg-muted-foreground/40",
};

export function OverviewPage() {
  const [window, setWindow] = useState("24h");
  const poll = usePollInterval(REFRESH_MS);
  const stats = useQuery({ queryKey: ["stats", window], queryFn: () => api.stats(window), refetchInterval: poll });
  const activity = useQuery({ queryKey: ["stats", "activity", window], queryFn: () => api.activity(window), refetchInterval: poll });
  const recent = useQuery({ queryKey: ["jobs", "recent"], queryFn: () => api.listJobs({ limit: 6 }), refetchInterval: poll });
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices, refetchInterval: poll });

  const s = stats.data;
  const a = activity.data;
  const rate = (t?: { delivered: number; failed: number }) =>
    t && t.delivered + t.failed > 0 ? (t.delivered / (t.delivered + t.failed)) * 100 : null;
  const rateNow = rate(a?.current);
  const ratePrev = rate(a?.previous);
  const previousLabel = WINDOWS.find((w) => w.value === window)?.previous ?? "la période d'avant";

  return (
    <>
      <PageHeader
        title="Vue d'ensemble"
        subtitle="Activité de la passerelle, mise à jour en direct."
        actions={
          <div role="tablist" aria-label="Période" className="inline-flex divide-x rounded-sm border bg-card">
            {WINDOWS.map((w) => (
              <button
                key={w.value}
                role="tab"
                aria-selected={window === w.value}
                onClick={() => setWindow(w.value)}
                className={cn(
                  "px-3 py-1 font-mono text-xs font-medium transition-colors",
                  window === w.value
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )}
              >
                {w.label}
              </button>
            ))}
          </div>
        }
      />

      <ErrorNote error={stats.error ?? activity.error} />

      {s && devices.data && <HealthBanner devices={devices.data.devices} queued={s.jobs.pending + s.jobs.assigned} />}

      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-md border bg-border lg:grid-cols-4">
        <Kpi
          label="Messages"
          value={a ? String(a.current.total) : undefined}
          delta={a ? pctDelta(a.current.total, a.previous.total) : null}
          deltaGood="up"
          hint={`vs ${previousLabel}`}
        />
        <Kpi
          label="Taux de succès"
          value={!a ? undefined : rateNow === null ? "—" : `${Math.round(rateNow)} %`}
          delta={rateNow !== null && ratePrev !== null ? { value: rateNow - ratePrev, unit: "pts" } : null}
          deltaGood="up"
          hint="envoyés / terminés"
        />
        <Kpi
          label="Appareils prêts"
          value={s ? `${s.devices.ready} / ${s.devices.total}` : undefined}
          hint={s ? `${s.devices.connected} connecté${s.devices.connected > 1 ? "s" : ""}` : undefined}
          meter={s && s.devices.total > 0 ? s.devices.ready / s.devices.total : undefined}
          tag={s && s.devices.total > 0 && s.devices.ready === 0 ? { text: "Critique", tone: "danger" } : undefined}
        />
        <Kpi
          label="En file"
          value={s ? String(s.jobs.pending + s.jobs.assigned) : undefined}
          hint={s && s.devices.ready === 0 && s.jobs.pending + s.jobs.assigned > 0 ? "en attente d'un appareil prêt" : "en attente ou assignés"}
          tag={s && s.devices.ready === 0 && s.jobs.pending + s.jobs.assigned > 0 ? { text: "Bloqué", tone: "warning" } : undefined}
        />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <section className="card overflow-hidden lg:col-span-2">
          <CardHeader title="Activité" description="Messages soumis, par issue." />
          <div className="p-4">
            {a ? (
              <ActivityChart buckets={a.buckets} bucketSeconds={a.bucketSeconds} />
            ) : (
              <Skeleton className="h-[260px] rounded-sm" />
            )}
          </div>
        </section>

        <FleetPanel devices={devices.data?.devices} />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <section className="card overflow-hidden lg:col-span-2">
          <CardHeader
            title="Derniers messages"
            actions={
              <Link to="/history" className="inline-flex items-center gap-1 text-[13px] font-medium underline-offset-4 hover:underline">
                Tout l'historique <ArrowRight className="size-3.5" aria-hidden />
              </Link>
            }
          />
          {recent.error && (
            <div className="p-4">
              <ErrorNote error={recent.error} />
            </div>
          )}
          {recent.data && recent.data.jobs.length === 0 && (
            <Empty icon={<Inbox />}>
              Aucun message pour l'instant.{" "}
              <Link to="/send" className="font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground">
                Envoyer le premier
              </Link>
            </Empty>
          )}
          {!recent.data && !recent.error && <SkeletonRows rows={4} />}
          <ul className="divide-y">
            {recent.data?.jobs.map((j) => (
              <li key={j.jobId}>
                <Link to={`/history?job=${j.jobId}`} className="flex items-center gap-4 px-4 py-3 transition-colors hover:bg-row-hover">
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-sm">{j.recipient}</div>
                    <div className="truncate text-sm text-muted-foreground">{j.body}</div>
                  </div>
                  <StatusBadge status={j.status} />
                  <div className="hidden w-28 text-right text-xs text-muted-foreground sm:block">{formatRelative(j.createdAt)}</div>
                </Link>
              </li>
            ))}
          </ul>
        </section>

        <StatusBreakdown counts={s?.jobs} />
      </div>
    </>
  );
}

/**
 * The fleet's health in one line, at three levels: nothing can send (no
 * phone ready), something to look at while sending goes on (a phone off,
 * a battery low), or all is well.
 */
function HealthBanner({ devices, queued }: { devices: Device[]; queued: number }) {
  const enabled = devices.filter((d) => d.enabled);
  const ready = enabled.filter((d) => d.ready);
  const offline = enabled.filter((d) => !d.connected);
  const lowBattery = enabled.filter((d) => d.connected && d.health && d.health.batteryLevel < 15 && !d.health.isCharging);
  const plural = (n: number, word: string) => `${n} ${word}${n > 1 ? "s" : ""}`;
  const waiting = queued > 0 ? `${plural(queued, "message")} en attente` : "file vide";

  let level: "ok" | "warn" | "bad";
  let title: string;
  let detail: string;
  if (enabled.length === 0) {
    level = "bad";
    title = "Aucun téléphone dans la flotte";
    detail = "Ajoutez-en un pour envoyer : les messages attendent jusque-là.";
  } else if (ready.length === 0) {
    const last = [...enabled].filter((d) => d.lastSeenAt).sort((a, b) => (b.lastSeenAt ?? "").localeCompare(a.lastSeenAt ?? ""))[0];
    level = "bad";
    title = "Aucun appareil en ligne";
    detail = waiting + (last ? ` · ${last.label || "un téléphone"} vu ${formatRelative(last.lastSeenAt)}` : "");
  } else if (offline.length > 0) {
    level = "warn";
    title = `${plural(offline.length, "appareil")} hors ligne`;
    detail = `${plural(ready.length, "appareil")} prêt${ready.length > 1 ? "s" : ""} — les envois continuent · ${waiting}`;
  } else if (lowBattery.length > 0) {
    level = "warn";
    title = "Batterie faible";
    detail = `${plural(lowBattery.length, "appareil")} sous 15 % — les envois continuent`;
  } else {
    level = "ok";
    title = "Tout est opérationnel";
    detail = `${plural(ready.length, "appareil")} en ligne · ${waiting}`;
  }

  const Icon = level === "ok" ? CheckCircle2 : level === "warn" ? AlertTriangle : XCircle;
  return (
    <div
      role="status"
      className={cn(
        "mb-4 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-sm border border-l-[3px] bg-card px-4 py-3 text-sm",
        level === "ok" && "border-l-success",
        level === "warn" && "border-l-warning",
        level === "bad" && "border-l-destructive bg-danger-soft",
      )}
    >
      <Icon
        className={cn("size-[18px] shrink-0", level === "ok" ? "text-success" : level === "warn" ? "text-warning" : "text-destructive")}
        aria-hidden
      />
      <span className="font-semibold">{title}</span>
      <span className="text-muted-foreground">{detail}</span>
      {level !== "ok" && (
        <Link to="/devices" className="ml-auto inline-flex items-center gap-1 font-medium text-foreground hover:underline">
          Voir la flotte <ArrowRight className="size-3.5" aria-hidden />
        </Link>
      )}
    </div>
  );
}

function pctDelta(now: number, before: number): { value: number; unit: "%" } | null {
  if (before === 0) return null;
  return { value: ((now - before) / before) * 100, unit: "%" };
}

function Kpi({
  label,
  value,
  hint,
  delta,
  deltaGood,
  meter,
  tag,
}: {
  label: string;
  /** Undefined while loading: a placeholder bar holds the figure's place. */
  value?: string;
  hint?: string;
  delta?: { value: number; unit: string } | null;
  deltaGood?: "up" | "down";
  meter?: number;
  /** A word that says the figure needs attention: "Critique", "Bloqué". */
  tag?: { text: string; tone: "danger" | "warning" };
}) {
  const flat = delta && Math.abs(delta.value) < 0.5;
  const up = delta && delta.value > 0;
  const good = delta && !flat && (deltaGood === "up" ? up : !up);
  return (
    <div className="bg-card p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="eyebrow">{label}</span>
        {tag && (
          <span className={cn("font-mono text-[11px] font-medium uppercase", tag.tone === "danger" ? "text-destructive" : "text-warning")}>
            {tag.text}
          </span>
        )}
      </div>
      <div className="mt-3 flex items-baseline gap-2">
        {value === undefined ? (
          <Skeleton className="h-8 w-20 rounded-sm" />
        ) : (
          <span className="font-mono text-[1.875rem] leading-none font-medium tracking-tight">{value}</span>
        )}
        {delta && (
          <span
            className={cn(
              "inline-flex items-center gap-0.5 font-mono text-xs font-medium",
              flat ? "text-muted-foreground" : good ? "text-success" : "text-destructive",
            )}
            title="Par rapport à la période précédente"
          >
            {!flat && (up ? <ArrowUpRight className="size-3" aria-hidden /> : <ArrowDownRight className="size-3" aria-hidden />)}
            {delta.value > 0 ? "+" : ""}
            {Math.round(delta.value)} {delta.unit}
          </span>
        )}
      </div>
      {meter !== undefined && (
        <div className="mt-3 h-1 overflow-hidden bg-muted" aria-hidden>
          <div className="h-full bg-foreground/70 transition-[width]" style={{ width: `${Math.round(meter * 100)}%` }} />
        </div>
      )}
      {hint && <div className={cn("text-xs text-muted-foreground", meter !== undefined ? "mt-2" : "mt-2.5")}>{hint}</div>}
    </div>
  );
}

/** One bar for the whole distribution, then the list it stands for. */
function StatusBreakdown({ counts }: { counts?: Record<JobStatus, number> }) {
  const total = counts ? JOB_STATUSES.reduce((n, st) => n + counts[st], 0) : 0;
  return (
    <section className="card overflow-hidden">
      <CardHeader title="Par statut" description="Sur la période choisie." />
      <div className="p-4">
        <div className="flex h-2 gap-px overflow-hidden bg-muted" aria-hidden>
          {counts &&
            total > 0 &&
            JOB_STATUSES.filter((st) => counts[st] > 0).map((st) => (
              <div key={st} className={cn("h-full", TONE_BG[STATUS_TONE[st]])} style={{ width: `${(counts[st] / total) * 100}%` }} />
            ))}
        </div>
        <ul className="mt-4 space-y-0.5">
          {JOB_STATUSES.map((st) => (
            <li key={st}>
              <Link
                to={`/history?status=${st}`}
                className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm transition-colors hover:bg-row-hover"
              >
                <span className={cn("size-2", TONE_BG[STATUS_TONE[st]])} aria-hidden />
                <span className="text-muted-foreground">{STATUS_LABEL[st]}</span>
                <span className="ml-auto font-mono font-medium">{counts ? counts[st] : "—"}</span>
                <span className="w-10 text-right text-xs text-muted-foreground tabular-nums">
                  {counts && total > 0 ? `${Math.round((counts[st] / total) * 100)} %` : ""}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

/** The fleet at a glance: who is up, how charged, how loaded against its caps. */
function FleetPanel({ devices }: { devices?: Device[] }) {
  return (
    <section className="card flex flex-col overflow-hidden">
      <CardHeader
        title="Flotte"
        actions={
          <Link to="/devices" className="inline-flex items-center gap-1 text-[13px] font-medium underline-offset-4 hover:underline">
            Gérer <ArrowRight className="size-3.5" aria-hidden />
          </Link>
        }
      />
      {devices && devices.length === 0 && (
        <Empty icon={<Smartphone />}>
          Aucun téléphone.{" "}
          <Link to="/devices" className="font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground">
            En ajouter un
          </Link>
        </Empty>
      )}
      {!devices && <SkeletonRows rows={3} />}
      <ul className="divide-y">
        {devices?.map((d) => {
          const cap = d.policy?.limits?.perHour;
          const used = d.usage?.lastHour ?? 0;
          return (
            <li key={d.deviceId} className="px-4 py-3">
              <div className="flex items-center gap-2">
                <span
                  className={cn(
                    "size-2 shrink-0",
                    d.ready ? "bg-success" : d.connected ? "bg-warning" : "bg-muted-foreground/40",
                  )}
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{d.label || "Sans nom"}</span>
                {d.health && <SignalBars level={d.health.signalStrength} />}
              </div>
              <div className="mt-1.5 flex items-center gap-3 pl-4 text-xs text-muted-foreground">
                <span>{d.ready ? "Prêt" : d.connected ? "Connecté, pas prêt" : `Vu ${formatRelative(d.lastSeenAt)}`}</span>
                {d.health && (
                  <span className="inline-flex items-center gap-1">
                    {d.health.isCharging ? <BatteryCharging className="size-3.5" aria-hidden /> : <BatteryMedium className="size-3.5" aria-hidden />}
                    {d.health.batteryLevel} %
                  </span>
                )}
                <span className="ml-auto font-mono">
                  {used}
                  {cap ? ` / ${cap}` : ""} · 1 h
                </span>
              </div>
              {cap ? (
                <div className="mt-2 ml-4 h-1 overflow-hidden bg-muted" aria-hidden>
                  <div
                    className={cn("h-full", used >= cap ? "bg-warning" : "bg-foreground/70")}
                    style={{ width: `${Math.min(100, (used / cap) * 100)}%` }}
                  />
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** Placeholder rows while a list loads, shaped like the rows that will come. */
function SkeletonRows({ rows }: { rows: number }) {
  return (
    <ul className="divide-y" aria-hidden>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="flex items-center gap-4 px-4 py-3">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-32 rounded-sm" />
            <Skeleton className="h-3 w-3/4 rounded-sm" />
          </div>
          <Skeleton className="h-5 w-16 rounded-sm" />
        </li>
      ))}
    </ul>
  );
}
