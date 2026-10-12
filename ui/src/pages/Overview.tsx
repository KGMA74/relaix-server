import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CheckCircle2, Inbox, MessageSquare, Smartphone, Timer, type LucideIcon } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { JOB_STATUSES } from "../api/types";
import { CardHeader, Empty, ErrorNote, PageHeader, STATUS_LABEL, STATUS_TONE, StatusBadge, type Tone } from "../components/common";
import { formatRelative } from "../lib/format";
import { usePollInterval } from "@/lib/live";
import { cn } from "@/lib/utils";

const WINDOWS = [
  { value: "1h", label: "1 h" },
  { value: "24h", label: "24 h" },
  { value: "168h", label: "7 j" },
  { value: "720h", label: "30 j" },
];

const REFRESH_MS = 10_000;

const TONE_TEXT: Record<Tone, string> = {
  success: "text-success",
  warning: "text-warning",
  danger: "text-destructive",
  info: "text-info",
  primary: "text-primary",
  neutral: "text-muted-foreground",
};

export function OverviewPage() {
  const [window, setWindow] = useState("24h");
  const poll = usePollInterval(REFRESH_MS);
  const stats = useQuery({ queryKey: ["stats", window], queryFn: () => api.stats(window), refetchInterval: poll });
  const recent = useQuery({ queryKey: ["jobs", "recent"], queryFn: () => api.listJobs({ limit: 8 }), refetchInterval: poll });

  const s = stats.data;
  const delivered = s ? s.jobs.sent + s.jobs.delivered : 0;
  const finished = s ? delivered + s.jobs.failed : 0;
  const successRate = finished > 0 ? Math.round((delivered / finished) * 100) : null;

  return (
    <>
      <PageHeader
        title="Vue d'ensemble"
        subtitle="Activité de la passerelle, mise à jour en direct."
        actions={
          <div role="tablist" aria-label="Période" className="inline-flex rounded-md border bg-card p-0.5 shadow-xs">
            {WINDOWS.map((w) => (
              <button
                key={w.value}
                role="tab"
                aria-selected={window === w.value}
                onClick={() => setWindow(w.value)}
                className={cn(
                  "rounded-sm px-3 py-1 text-sm font-medium transition-colors",
                  window === w.value
                    ? "bg-primary text-primary-foreground shadow-xs"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )}
              >
                {w.label}
              </button>
            ))}
          </div>
        }
      />

      <ErrorNote error={stats.error} />

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Kpi icon={MessageSquare} label="Messages" value={s?.total} hint="soumis sur la période" />
        <Kpi
          icon={CheckCircle2}
          label="Taux de succès"
          value={successRate === null ? "—" : `${successRate} %`}
          hint="envoyés / terminés"
        />
        <Kpi
          icon={Smartphone}
          label="Appareils prêts"
          value={s ? `${s.devices.ready} / ${s.devices.total}` : undefined}
          hint={s ? `${s.devices.connected} connecté(s)` : ""}
        />
        <Kpi icon={Timer} label="En file" value={s ? s.jobs.pending + s.jobs.assigned : undefined} hint="en attente ou assignés" />
      </div>

      <div className="card mt-4 grid grid-cols-3 divide-x divide-y overflow-hidden sm:grid-cols-6 sm:divide-y-0">
        {JOB_STATUSES.map((st) => (
          <Link key={st} to={`/history?status=${st}`} className="group px-4 py-3 transition-colors hover:bg-row-hover">
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className={cn("size-1.5 rounded-full bg-current", TONE_TEXT[STATUS_TONE[st]])} aria-hidden />
              {STATUS_LABEL[st]}
            </div>
            <div className="mt-1 text-lg font-semibold tabular-nums">{s ? s.jobs[st] : "—"}</div>
          </Link>
        ))}
      </div>

      <div className="card mt-6 overflow-hidden">
        <CardHeader
          title="Derniers messages"
          actions={
            <Link to="/history" className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline">
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
            <Link to="/send" className="font-medium text-primary hover:underline">
              Envoyer le premier
            </Link>
          </Empty>
        )}
        <ul className="divide-y">
          {recent.data?.jobs.map((j) => (
            <li key={j.jobId}>
              <Link
                to={`/history?job=${j.jobId}`}
                className="flex items-center gap-4 px-4 py-3 transition-colors hover:bg-row-hover"
              >
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
      </div>
    </>
  );
}

function Kpi({ icon: Icon, label, value, hint }: { icon: LucideIcon; label: string; value?: string | number; hint?: string }) {
  return (
    <div className="card p-4">
      <div className="flex items-center justify-between text-sm text-muted-foreground">
        {label}
        <Icon className="size-4 text-muted-foreground/70" aria-hidden />
      </div>
      <div className="mt-2 text-2xl font-semibold tabular-nums">{value ?? "—"}</div>
      {hint && <div className="mt-1 text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}
