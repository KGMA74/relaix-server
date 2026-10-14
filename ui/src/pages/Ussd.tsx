import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Hash, Loader2, Lock, Play, RefreshCw } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { TERMINAL_STATUSES, type Job, type JobMode, type JobStatus, type UssdRequest } from "../api/types";
import { CardHeader, Empty, ErrorNote, PageHeader, Pill, STATUS_TONE } from "../components/common";
import { formatDateTime, shortId } from "../lib/format";
import { usePollInterval } from "@/lib/live";
import { cn } from "@/lib/utils";

// Matches the server (api/ussd.go).
const MAX_CODE = 182;
// {pin}: the SIM's PIN, filled in by the phone from its own vault.
const USSD_CODE = /^[*#]([0-9*#+]|\{pin\})*#$/;

/** What a status means for a code rather than a message. */
const USSD_LABEL: Record<JobStatus, string> = {
  pending: "En attente",
  assigned: "En cours",
  sent: "Répondu",
  delivered: "Répondu",
  failed: "Échoué",
  cancelled: "Annulé",
};

function UssdStatus({ status }: { status: JobStatus }) {
  return <Pill tone={STATUS_TONE[status]}>{USSD_LABEL[status]}</Pill>;
}

export function UssdPage() {
  const queryClient = useQueryClient();
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });
  const capable = devices.data?.devices.filter((d) => d.capabilities?.includes("ussd")) ?? [];

  const [deviceId, setDeviceId] = useState("");
  const [subscriptionId, setSubscriptionId] = useState("");
  const [code, setCode] = useState("");
  const [repliesRaw, setRepliesRaw] = useState("");
  const [sensitive, setSensitive] = useState(false);
  const [mode, setMode] = useState<JobMode>("immediate");
  const [jobId, setJobId] = useState<string>();

  const device = capable.find((d) => d.deviceId === deviceId);
  const menus = device?.capabilities.includes("ussd_menu") ?? false;
  const replies = repliesRaw
    .split(/[,\n]/)
    .map((r) => r.trim())
    .filter(Boolean);
  const sentReplies = menus ? replies : [];
  const usesPin = code.includes("{pin}") || sentReplies.some((r) => r.includes("{pin}"));
  // What the phone approves a menu as: the all-in-one code it amounts to,
  // *144# answered 2, 1, {pin} being *144*2*1*{pin}#. Same rule as the agent.
  const session = sentReplies.length ? code.trim().replace(/#$/, "") + "*" + sentReplies.join("*") + "#" : code.trim();
  const codeOk = USSD_CODE.test(code.trim()) && code.trim().length <= MAX_CODE;

  const run = useMutation({
    mutationFn: (req: UssdRequest) => api.ussd(req),
    onSuccess: (job) => {
      setJobId(job.jobId);
      queryClient.invalidateQueries({ queryKey: ["jobs", "ussd"] });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const req: UssdRequest = { deviceId, code: code.trim(), mode, sensitive };
    if (subscriptionId) req.subscriptionId = Number(subscriptionId);
    if (menus && replies.length) req.replies = replies;
    run.mutate(req);
  }

  const canRun = deviceId !== "" && codeOk && !run.isPending && (!replies.length || menus);

  return (
    <>
      <PageHeader title="USSD" subtitle="Exécuter un code (*144#, solde, forfait…) sur la SIM d'un téléphone et lire la réponse du réseau." />

      <div className="grid gap-6 lg:grid-cols-3">
        <form onSubmit={submit} className="card space-y-5 p-6 lg:col-span-2">
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label className="label" htmlFor="ussd-device">
                Appareil
              </label>
              <select
                id="ussd-device"
                className="input"
                value={deviceId}
                onChange={(e) => {
                  setDeviceId(e.target.value);
                  setSubscriptionId("");
                }}
              >
                <option value="">Choisir un téléphone</option>
                {capable.map((d) => (
                  <option key={d.deviceId} value={d.deviceId} disabled={!d.enabled}>
                    {d.label || shortId(d.deviceId)} {d.ready ? "· prêt" : d.connected ? "· connecté" : "· hors ligne"}
                  </option>
                ))}
              </select>
              {devices.isSuccess && capable.length === 0 && (
                <p className="mt-1 text-xs text-muted-foreground">
                  Aucun téléphone ne sait exécuter de code : mettez l'application à jour sur vos appareils.
                </p>
              )}
            </div>
            <div>
              <label className="label" htmlFor="ussd-sim">
                SIM
              </label>
              <select
                id="ussd-sim"
                className="input"
                value={subscriptionId}
                onChange={(e) => setSubscriptionId(e.target.value)}
                disabled={!device?.sims?.length}
              >
                <option value="">SIM par défaut</option>
                {device?.sims?.map((s) => (
                  <option key={s.subscriptionId} value={s.subscriptionId}>
                    Slot {s.slotIndex + 1} — {s.carrier || s.displayName || "SIM"} {s.phoneNumber ?? ""}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <label className="label" htmlFor="ussd-code">
              Code
            </label>
            <input
              id="ussd-code"
              className="input font-mono"
              placeholder="*144#"
              value={code}
              maxLength={MAX_CODE}
              onChange={(e) => setCode(e.target.value)}
            />
            {code.trim() !== "" && !codeOk && (
              <p className="mt-1 text-xs text-destructive">Un code commence par * ou #, finit par #, et ne contient que des chiffres, * et #.</p>
            )}
            {usesPin ? (
              <p className="mt-1 text-xs text-muted-foreground">
                <code>{"{pin}"}</code> sera remplacé par le téléphone avec le PIN de la SIM, gardé chez lui — seulement si ce modèle de code y a
                été approuvé, dans ses plafonds.
                {sentReplies.length > 0 && (
                  <>
                    {" "}
                    Avec des réponses, le modèle à approuver sur le téléphone est la session entière :{" "}
                    <code className="break-all">{session}</code> (un montant ou un numéro variable s'y écrit <code>{"{amount}"}</code>,{" "}
                    <code>{"{to}"}</code>).
                  </>
                )}
                {device && !device.capabilities.includes("ussd_pin") && " L'app de ce téléphone doit être mise à jour pour cela."}
              </p>
            ) : (
              <p className="mt-1 text-xs text-muted-foreground">
                Besoin du PIN ? Écrivez <code>{"{pin}"}</code> à sa place : il ne quitte jamais le téléphone.
              </p>
            )}
          </div>

          <div>
            <label className="label" htmlFor="ussd-replies">
              Réponses aux menus <span className="font-normal text-muted-foreground">(facultatif)</span>
            </label>
            <input
              id="ussd-replies"
              className="input font-mono"
              placeholder="2, 1, 70000000, 5000, {pin}"
              value={repliesRaw}
              disabled={!menus}
              onChange={(e) => setRepliesRaw(e.target.value)}
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {menus
                ? "Tapées dans l'ordre, une par écran, comme vous le feriez à la main. Le PIN demandé à un écran s'écrit {pin}."
                : "Ce téléphone n'exécute qu'un code complet : préférez la forme tout-en-un (*144*2*1*…#)."}
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <label className="flex cursor-pointer items-start gap-3 rounded-md border p-3 text-sm">
              <input type="checkbox" className="mt-0.5 accent-primary" checked={sensitive} onChange={(e) => setSensitive(e.target.checked)} />
              <span>
                <span className="font-medium">Contient un code secret</span>
                <span className="block text-muted-foreground">Code et réponses masqués partout, puis effacés une fois terminé.</span>
              </span>
            </label>
            <label className="flex cursor-pointer items-start gap-3 rounded-md border p-3 text-sm">
              <input
                type="checkbox"
                className="mt-0.5 accent-primary"
                checked={mode === "queued"}
                onChange={(e) => setMode(e.target.checked ? "queued" : "immediate")}
              />
              <span>
                <span className="font-medium">Attendre le téléphone</span>
                <span className="block text-muted-foreground">Sinon, échoue tout de suite s'il n'est pas prêt.</span>
              </span>
            </label>
          </div>

          <ErrorNote error={run.error} />

          <div className="flex justify-end">
            <button type="submit" className="btn-primary" disabled={!canRun}>
              {run.isPending ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Play className="size-4" aria-hidden />}
              Exécuter
            </button>
          </div>
        </form>

        <aside className="card h-fit overflow-hidden">
          <CardHeader title="Réponse du réseau" />
          {jobId ? <UssdResult jobId={jobId} /> : <Empty icon={<Hash />}>La réponse s'affichera ici.</Empty>}
        </aside>
      </div>

      <UssdHistory onOpen={setJobId} deviceLabel={(id) => devices.data?.devices.find((d) => d.deviceId === id)?.label || shortId(id)} />
    </>
  );
}

function UssdResult({ jobId }: { jobId: string }) {
  const poll = usePollInterval(2000);
  const job = useQuery({
    queryKey: ["job", jobId],
    queryFn: () => api.getJob(jobId),
    refetchInterval: (q) => (q.state.data && TERMINAL_STATUSES.includes(q.state.data.status) ? false : poll),
  });
  const j = job.data;
  if (!j) return job.error ? <ErrorNote error={job.error} /> : <Empty icon={<Loader2 className="animate-spin" />}>Envoi au téléphone…</Empty>;

  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-sm">{j.sensitive ? <Lock className="inline size-3.5" aria-label="masqué" /> : j.body}</span>
        <UssdStatus status={j.status} />
      </div>
      <Screens job={j} />
      {!TERMINAL_STATUSES.includes(j.status) && (
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="size-3 animate-spin" aria-hidden /> En attente de la réponse du réseau…
        </p>
      )}
    </div>
  );
}

function Screens({ job }: { job: Job }) {
  return (
    <>
      {job.responses?.map((r, i) => (
        <div key={i} className="rounded-md border bg-muted/50 p-3 font-mono text-sm whitespace-pre-wrap">
          {r}
        </div>
      ))}
      {(job.errorCode || job.errorMessage) && <ErrorNote error={new Error([job.errorCode, job.errorMessage].filter(Boolean).join(" — "))} />}
    </>
  );
}

function UssdHistory({ onOpen, deviceLabel }: { onOpen: (id: string) => void; deviceLabel: (id: string) => string }) {
  const jobs = useQuery({ queryKey: ["jobs", "ussd"], queryFn: () => api.listJobs({ kind: "ussd", limit: 20 }) });
  const rows = jobs.data?.jobs ?? [];

  return (
    <div className="card mt-6 overflow-hidden">
      <CardHeader
        title="Derniers codes"
        actions={
          <button className="btn-secondary" onClick={() => jobs.refetch()} disabled={jobs.isFetching}>
            <RefreshCw className={cn("size-4", jobs.isFetching && "animate-spin")} aria-hidden />
            Actualiser
          </button>
        }
      />
      <ErrorNote error={jobs.error} />
      {jobs.isSuccess && rows.length === 0 ? (
        <Empty icon={<Hash />}>Aucun code exécuté pour l'instant.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="border-b bg-muted/50">
              <tr>
                <th className="th">Date</th>
                <th className="th">Code</th>
                <th className="th">Réponse</th>
                <th className="th">Statut</th>
                <th className="th">Appareil</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((j) => (
                <tr key={j.jobId} onClick={() => onOpen(j.jobId)} className="cursor-pointer transition-colors hover:bg-row-hover">
                  <td className="td whitespace-nowrap text-muted-foreground">{formatDateTime(j.createdAt)}</td>
                  <td className="td whitespace-nowrap font-mono">{j.sensitive ? <Lock className="size-3.5" aria-label="masqué" /> : j.body}</td>
                  <td className="td max-w-md truncate text-muted-foreground">{j.responses?.at(-1) ?? j.errorMessage ?? "—"}</td>
                  <td className="td">
                    <UssdStatus status={j.status} />
                  </td>
                  <td className="td whitespace-nowrap text-muted-foreground">{j.deviceId ? deviceLabel(j.deviceId) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
