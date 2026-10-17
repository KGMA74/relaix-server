import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Inbox, Loader2, RefreshCw } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { cn } from "@/lib/utils";
import { api, type JobQuery } from "../api/client";
import { JOB_STATUSES, TERMINAL_STATUSES, type Job, type JobStatus } from "../api/types";
import { ConfirmDialog, Empty, ErrorNote, Modal, ModalActions, PageHeader, STATUS_LABEL, StatusBadge } from "../components/common";
import { formatDateTime, localInputToISO, shortId } from "../lib/format";
import { usePollInterval } from "@/lib/live";

const PAGE_SIZE = 50;

export function HistoryPage() {
  // Filters live in the URL, so a filtered view can be reloaded, bookmarked or
  // linked to — the overview's status counts link straight into one.
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const deviceId = params.get("deviceId") ?? "";
  const recipient = params.get("recipient") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };

  // SMS only: USSD jobs have their own page, where their answers make sense.
  const filter: JobQuery = {
    kind: "sms",
    status: status ? [status] : undefined,
    deviceId: deviceId || undefined,
    recipient: recipient || undefined,
    from: localInputToISO(from),
    to: localInputToISO(to),
    limit: PAGE_SIZE,
  };

  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });
  const deviceLabel = (id?: string) => {
    if (!id) return "—";
    const d = devices.data?.devices.find((x) => x.deviceId === id);
    return d?.label || shortId(id);
  };

  const jobs = useInfiniteQuery({
    queryKey: ["jobs", "history", filter],
    queryFn: ({ pageParam }) => api.listJobs({ ...filter, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });

  const rows = jobs.data?.pages.flatMap((p) => p.jobs) ?? [];
  // The open job lives in the URL too, so the overview can link straight to one.
  const selectedId = params.get("job");
  const selected = rows.find((j) => j.jobId === selectedId);
  const hasFilter = status || deviceId || recipient || from || to;

  return (
    <>
      <PageHeader
        title="Historique"
        subtitle="Tous les messages soumis, du plus récent au plus ancien."
        actions={
          <button className="btn-secondary" onClick={() => jobs.refetch()} disabled={jobs.isFetching}>
            <RefreshCw className={jobs.isFetching ? "size-4 animate-spin" : "size-4"} aria-hidden />
            Actualiser
          </button>
        }
      />

      <div className="card mb-4 grid gap-3 p-4 sm:grid-cols-2 lg:grid-cols-5">
        <select className="input" value={status} onChange={(e) => setFilter("status", e.target.value)} aria-label="Statut">
          <option value="">Tous les statuts</option>
          {JOB_STATUSES.map((s) => (
            <option key={s} value={s}>
              {STATUS_LABEL[s]}
            </option>
          ))}
        </select>
        <select className="input" value={deviceId} onChange={(e) => setFilter("deviceId", e.target.value)} aria-label="Appareil">
          <option value="">Tous les appareils</option>
          {devices.data?.devices.map((d) => (
            <option key={d.deviceId} value={d.deviceId}>
              {d.label || shortId(d.deviceId)}
            </option>
          ))}
        </select>
        <input
          className="input font-mono"
          placeholder="Numéro ou préfixe (+237…)"
          value={recipient}
          onChange={(e) => setFilter("recipient", e.target.value.trim())}
          aria-label="Destinataire"
        />
        <input type="datetime-local" className="input" value={from} onChange={(e) => setFilter("from", e.target.value)} aria-label="Depuis" title="Depuis" />
        <input type="datetime-local" className="input" value={to} onChange={(e) => setFilter("to", e.target.value)} aria-label="Jusqu'à" title="Jusqu'à" />
      </div>

      <ErrorNote error={jobs.error} />

      <div className="card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="border-b bg-muted/50">
              <tr>
                <th className="th">Date</th>
                <th className="th">Destinataire</th>
                <th className="th">Message</th>
                <th className="th">Statut</th>
                <th className="th">Appareil</th>
                <th className="th text-right">Parts</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((j) => (
                <tr
                  key={j.jobId}
                  onClick={() => setFilter("job", j.jobId)}
                  aria-selected={j.jobId === selectedId}
                  // The row open in the drawer keeps an accent edge, so the eye finds it again.
                  className={cn(
                    "cursor-pointer transition-colors hover:bg-row-hover",
                    j.jobId === selectedId && "bg-primary-soft shadow-[inset_2px_0_0_var(--primary)]",
                  )}
                >
                  <td className="td whitespace-nowrap text-muted-foreground">{formatDateTime(j.createdAt)}</td>
                  <td className="td whitespace-nowrap font-mono">{j.recipient}</td>
                  <td className="td max-w-xs truncate text-muted-foreground">{j.body}</td>
                  <td className="td">
                    <StatusBadge status={j.status} />
                  </td>
                  <td className="td whitespace-nowrap text-muted-foreground">
                    {deviceLabel(j.deviceId)}
                    {(j.sentFromSubscriptionId ?? j.subscriptionId) !== undefined && (
                      <span className="text-xs"> · SIM {j.sentFromSubscriptionId ?? j.subscriptionId}</span>
                    )}
                  </td>
                  <td className="td text-right tabular-nums">{j.partsSent || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {jobs.isSuccess && rows.length === 0 && <Empty icon={<Inbox />}>{hasFilter ? "Aucun message ne correspond aux filtres." : "Aucun message pour l'instant."}</Empty>}
        {jobs.isLoading && (
          <Empty icon={<Loader2 className="animate-spin" />}>Chargement…</Empty>
        )}
      </div>

      {jobs.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <button className="btn-secondary" onClick={() => jobs.fetchNextPage()} disabled={jobs.isFetchingNextPage}>
            {jobs.isFetchingNextPage ? "Chargement…" : "Charger plus"}
          </button>
        </div>
      )}

      {selectedId && (
        <JobDetail jobId={selectedId} initial={selected} deviceLabel={deviceLabel} onClose={() => setFilter("job", "")} />
      )}
    </>
  );
}

function JobDetail({
  jobId,
  initial,
  deviceLabel,
  onClose,
}: {
  jobId: string;
  initial?: Job;
  deviceLabel: (id?: string) => string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  // Re-read while open: a pending job's status can move under the operator's eyes.
  const poll = usePollInterval(5000);
  const job = useQuery({ queryKey: ["job", jobId], queryFn: () => api.getJob(jobId), initialData: initial, refetchInterval: poll });
  const events = useQuery({ queryKey: ["job", jobId, "events"], queryFn: () => api.jobEvents(jobId), refetchInterval: poll });
  const [confirming, setConfirming] = useState(false);

  const cancel = useMutation({
    mutationFn: () => api.cancelJob(jobId),
    onSuccess: () => {
      setConfirming(false);
      queryClient.invalidateQueries({ queryKey: ["job", jobId] });
      queryClient.invalidateQueries({ queryKey: ["jobs"] });
      queryClient.invalidateQueries({ queryKey: ["stats"] });
    },
  });

  const j = job.data;
  if (!j) {
    return (
      <Modal onClose={onClose} wide label="Message">
        {job.error ? <ErrorNote error={job.error} /> : <Empty icon={<Loader2 className="animate-spin" />}>Chargement…</Empty>}
      </Modal>
    );
  }
  const cancellable = !TERMINAL_STATUSES.includes(j.status);

  return (
    <Modal onClose={onClose} wide label="Message" description={<span className="font-mono text-xs">{j.jobId}</span>}>
      <div className="flex items-center justify-between gap-4">
        <span className="font-mono text-lg font-semibold">{j.recipient}</span>
        <StatusBadge status={j.status} />
      </div>

      <div className="mt-4 rounded-md border bg-muted/50 p-3 text-sm whitespace-pre-wrap">{j.body}</div>

      {(j.errorCode || j.errorMessage) && (
        <div className="mt-3">
          <ErrorNote error={new Error([j.errorCode, j.errorMessage].filter(Boolean).join(" — "))} />
        </div>
      )}

      <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
        <Field label="Créé">{formatDateTime(j.createdAt)}</Field>
        <Field label="Terminé">{formatDateTime(j.completedAt)}</Field>
        <Field label="Programmé">{formatDateTime(j.scheduledAt)}</Field>
        <Field label="Appareil">{deviceLabel(j.deviceId)}</Field>
        <Field label="SIM">
          {j.sentFromSubscriptionId ?? j.subscriptionId ?? "par défaut"}
          {j.subscriptionId === undefined && j.sentFromSubscriptionId !== undefined && (
            <span className="text-xs text-muted-foreground"> (choisie par les règles)</span>
          )}
        </Field>
        <Field label="Mode">{j.mode === "immediate" ? "Immédiat" : "En file"}</Field>
        <Field label="Priorité">{j.priority}</Field>
        <Field label="Tentatives">{j.attempts}</Field>
        <Field label="Parts envoyées">{j.partsSent}</Field>
      </dl>

      <h3 className="mt-6 mb-3 eyebrow">Chronologie</h3>
      <ErrorNote error={events.error} />
      <ol className="relative ml-2 border-l">
        {events.data?.events.map((e, i) => (
          <li key={i} className="mb-3 ml-4">
            <span className="absolute -left-1.5 mt-1.5 size-3 border-2 border-background bg-foreground" />
            <div className="flex flex-wrap items-center gap-2 text-sm">
              {e.status ? <StatusBadge status={e.status as JobStatus} /> : <span className="text-xs text-muted-foreground">événement</span>}
              <span className="text-xs text-muted-foreground">{formatDateTime(e.createdAt)}</span>
              {e.deviceId && <span className="text-xs text-muted-foreground">· {deviceLabel(e.deviceId)}</span>}
            </div>
            {e.reason && <p className="mt-0.5 text-sm text-muted-foreground">{e.reason}</p>}
          </li>
        ))}
        {events.data?.events.length === 0 && <li className="ml-4 text-sm text-muted-foreground">Aucun événement enregistré.</li>}
      </ol>

      <ModalActions>
        {cancellable && (
          <button className="btn-danger" onClick={() => setConfirming(true)}>
            Annuler l'envoi
          </button>
        )}
        <button className="btn-secondary" onClick={onClose}>
          Fermer
        </button>
      </ModalActions>

      {confirming && (
        <ConfirmDialog
          title="Annuler ce message ?"
          body={
            <>
              Le job vers <span className="font-mono">{j.recipient}</span> ne sera pas envoyé. Si un téléphone l'a déjà transmis au réseau,
              l'annulation sera refusée.
              <div className="mt-3">
                <ErrorNote error={cancel.error} />
              </div>
            </>
          }
          confirmLabel="Annuler l'envoi"
          busy={cancel.isPending}
          onConfirm={() => cancel.mutate()}
          onCancel={() => setConfirming(false)}
        />
      )}
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5">{children}</dd>
    </div>
  );
}
