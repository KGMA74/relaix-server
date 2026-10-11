import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, History, Loader2, Send, SquarePen } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { Job, JobMode, SendRequest } from "../api/types";
import { CardHeader, Empty, ErrorNote, PageHeader, StatusBadge } from "../components/common";
import { cn } from "@/lib/utils";
import { localInputToISO, shortId } from "../lib/format";
import { countSegments, isE164, parseRecipients } from "../lib/sms";

// Matches the server's caps (api/jobs.go): fanout and body length.
const MAX_RECIPIENTS = 100;
const MAX_BODY = 1530;

export function SendPage() {
  const queryClient = useQueryClient();
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });

  const [recipientsRaw, setRecipientsRaw] = useState("");
  const [body, setBody] = useState("");
  const [deviceId, setDeviceId] = useState("");
  const [subscriptionId, setSubscriptionId] = useState("");
  const [mode, setMode] = useState<JobMode>("queued");
  const [priority, setPriority] = useState(0);
  const [scheduledAt, setScheduledAt] = useState("");
  const [callbackUrl, setCallbackUrl] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);

  const recipients = useMemo(() => parseRecipients(recipientsRaw), [recipientsRaw]);
  const invalid = recipients.filter((r) => !isE164(r));
  const segments = countSegments(body);
  const device = devices.data?.devices.find((d) => d.deviceId === deviceId);

  const send = useMutation({
    mutationFn: (req: SendRequest) => api.send(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["jobs"] });
      queryClient.invalidateQueries({ queryKey: ["stats"] });
    },
  });

  const created: Job[] = send.data ? ("jobs" in send.data ? send.data.jobs : [send.data]) : [];

  const tooMany = recipients.length > MAX_RECIPIENTS;
  const canSend =
    recipients.length > 0 && invalid.length === 0 && !tooMany && body.trim() !== "" && body.length <= MAX_BODY && !send.isPending;

  function submit(e: FormEvent) {
    e.preventDefault();
    const req: SendRequest = { body, mode };
    // The singular form when there is one number, so the reply is the plain
    // job object every other caller of POST /send gets.
    if (recipients.length === 1) req.recipient = recipients[0];
    else req.recipients = recipients;
    if (deviceId) req.deviceId = deviceId;
    if (deviceId && subscriptionId) req.subscriptionId = Number(subscriptionId);
    if (priority) req.priority = priority;
    const at = localInputToISO(scheduledAt);
    if (at) req.scheduledAt = at;
    if (callbackUrl.trim()) req.callbackUrl = callbackUrl.trim();
    send.mutate(req);
  }

  function reset() {
    setRecipientsRaw("");
    setBody("");
    send.reset();
  }

  return (
    <>
      <PageHeader title="Envoyer un SMS" subtitle="Un message, un ou plusieurs destinataires — chacun devient un job distinct." />

      <div className="grid gap-6 lg:grid-cols-3">
        <form onSubmit={submit} className="card space-y-5 p-6 lg:col-span-2">
          <div>
            <label className="label" htmlFor="recipients">
              Destinataires
            </label>
            <textarea
              id="recipients"
              className="input min-h-20 font-mono"
              placeholder="+237670000000, +33600000000"
              value={recipientsRaw}
              onChange={(e) => setRecipientsRaw(e.target.value)}
            />
            <div className="mt-1 flex justify-between text-xs text-muted-foreground">
              <span>Format E.164, séparés par des virgules, espaces ou retours à la ligne.</span>
              <span className={cn("tabular-nums", tooMany && "font-medium text-destructive")}>
                {recipients.length} / {MAX_RECIPIENTS}
              </span>
            </div>
            {invalid.length > 0 && <p className="mt-1 text-xs text-destructive">Numéro(s) invalide(s) : {invalid.join(", ")}</p>}
          </div>

          <div>
            <label className="label" htmlFor="body">
              Message
            </label>
            <textarea
              id="body"
              className="input min-h-32"
              value={body}
              maxLength={MAX_BODY}
              onChange={(e) => setBody(e.target.value)}
            />
            <div className="mt-1 flex justify-between text-xs text-muted-foreground">
              <span>
                {segments.encoding}
                {segments.encoding === "UCS-2" && " — un caractère hors alphabet GSM réduit chaque part à 70 caractères"}
              </span>
              <span className="tabular-nums">
                {segments.units} car. · {segments.parts} SMS · {segments.remaining} restants
              </span>
            </div>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label className="label" htmlFor="device">
                Appareil
              </label>
              <select
                id="device"
                className="input"
                value={deviceId}
                onChange={(e) => {
                  setDeviceId(e.target.value);
                  setSubscriptionId("");
                }}
              >
                <option value="">Automatique (le meilleur disponible)</option>
                {devices.data?.devices.map((d) => (
                  <option key={d.deviceId} value={d.deviceId} disabled={!d.enabled}>
                    {d.label || shortId(d.deviceId)} {d.ready ? "· prêt" : d.connected ? "· connecté" : "· hors ligne"}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label className="label" htmlFor="sim">
                SIM
              </label>
              <select
                id="sim"
                className="input"
                value={subscriptionId}
                onChange={(e) => setSubscriptionId(e.target.value)}
                disabled={!device?.sims?.length}
              >
                <option value="">SIM SMS par défaut</option>
                {device?.sims?.map((s) => (
                  <option key={s.subscriptionId} value={s.subscriptionId}>
                    Slot {s.slotIndex + 1} — {s.carrier || s.displayName || "SIM"} {s.phoneNumber ?? ""}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <span className="label">Mode</span>
            <div className="grid gap-2 sm:grid-cols-2">
              <ModeOption
                checked={mode === "queued"}
                onChange={() => setMode("queued")}
                title="En file"
                text="Attend qu'un appareil soit disponible et réessaie."
              />
              <ModeOption
                checked={mode === "immediate"}
                onChange={() => setMode("immediate")}
                title="Immédiat"
                text="Échoue tout de suite si aucun appareil n'est prêt (OTP)."
              />
            </div>
          </div>

          <button
            type="button"
            className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
            aria-expanded={showAdvanced}
            onClick={() => setShowAdvanced((v) => !v)}
          >
            <ChevronDown className={cn("size-4 transition-transform", showAdvanced && "rotate-180")} aria-hidden />
            Options avancées
          </button>

          {showAdvanced && (
            <div className="grid gap-4 sm:grid-cols-3">
              <div>
                <label className="label" htmlFor="scheduled">
                  Programmer
                </label>
                <input id="scheduled" type="datetime-local" className="input" value={scheduledAt} onChange={(e) => setScheduledAt(e.target.value)} />
              </div>
              <div>
                <label className="label" htmlFor="priority">
                  Priorité
                </label>
                <input id="priority" type="number" className="input" value={priority} onChange={(e) => setPriority(Number(e.target.value) || 0)} />
              </div>
              <div>
                <label className="label" htmlFor="callback">
                  Webhook
                </label>
                <input
                  id="callback"
                  type="url"
                  className="input"
                  placeholder="https://…"
                  value={callbackUrl}
                  onChange={(e) => setCallbackUrl(e.target.value)}
                />
              </div>
            </div>
          )}

          <ErrorNote error={send.error} />

          <div className="flex justify-end">
            <button type="submit" className="btn-primary" disabled={!canSend}>
              {send.isPending ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Send className="size-4" aria-hidden />}
              {send.isPending ? "Envoi…" : recipients.length > 1 ? `Envoyer à ${recipients.length} destinataires` : "Envoyer"}
            </button>
          </div>
        </form>

        <aside className="card h-fit overflow-hidden">
          <CardHeader title="Résultat" />
          {created.length === 0 ? (
            <Empty icon={<Send />}>Les jobs créés apparaîtront ici.</Empty>
          ) : (
            <div className="p-4">
              <p className="text-sm text-muted-foreground">
                {created.length} job(s) accepté(s). La passerelle a pris le message en charge ; son statut évolue dans l'historique.
              </p>
              <ul className="mt-3 divide-y text-sm">
                {created.map((j) => (
                  <li key={j.jobId} className="flex items-center justify-between gap-2 py-2">
                    <span className="font-mono">{j.recipient}</span>
                    <StatusBadge status={j.status} />
                  </li>
                ))}
              </ul>
              <div className="mt-4 flex gap-2">
                <Link to="/history" className="btn-secondary">
                  <History className="size-4" aria-hidden />
                  Historique
                </Link>
                <button className="btn-secondary" onClick={reset}>
                  <SquarePen className="size-4" aria-hidden />
                  Nouveau
                </button>
              </div>
            </div>
          )}
        </aside>
      </div>
    </>
  );
}

function ModeOption({ checked, onChange, title, text }: { checked: boolean; onChange: () => void; title: string; text: string }) {
  return (
    <label
      className={cn(
        "flex cursor-pointer gap-3 rounded-md border p-3 text-sm transition-colors",
        checked ? "border-primary bg-primary-soft" : "border-input hover:bg-row-hover",
      )}
    >
      <input type="radio" className="mt-0.5 accent-primary" checked={checked} onChange={onChange} />
      <span>
        <span className="font-medium">{title}</span>
        <span className="block text-muted-foreground">{text}</span>
      </span>
    </label>
  );
}
