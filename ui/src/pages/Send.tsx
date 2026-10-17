import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, History, Loader2, Send, SquarePen, X } from "lucide-react";
import { useMemo, useState, type ClipboardEvent, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { Device, Job, JobMode, SendRequest } from "../api/types";
import { CardHeader, ErrorNote, PageHeader, StatusBadge } from "../components/common";
import { cn } from "@/lib/utils";
import { localInputToISO, shortId } from "../lib/format";
import { countSegments, isE164, parseRecipients } from "../lib/sms";

// Matches the server's caps (api/jobs.go): fanout and body length.
const MAX_RECIPIENTS = 100;
const MAX_BODY = 1530;

type Route = "auto" | "device";

export function SendPage() {
  const queryClient = useQueryClient();
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });

  const [recipients, setRecipients] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [body, setBody] = useState("");
  const [route, setRoute] = useState<Route>("auto");
  const [deviceId, setDeviceId] = useState("");
  const [subscriptionId, setSubscriptionId] = useState("");
  const [mode, setMode] = useState<JobMode>("queued");
  const [priority, setPriority] = useState(0);
  const [scheduledAt, setScheduledAt] = useState("");
  const [callbackUrl, setCallbackUrl] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);

  // What is typed but not yet a chip still counts: nobody should lose a
  // number for not pressing Enter.
  const all = useMemo(() => dedupe([...recipients, ...parseRecipients(draft)]), [recipients, draft]);
  const invalid = all.filter((r) => !isE164(r));
  const segments = countSegments(body);
  const list = devices.data?.devices ?? [];
  const device = route === "device" ? list.find((d) => d.deviceId === deviceId) : undefined;
  const sim = device?.sims?.find((s) => String(s.subscriptionId) === subscriptionId);
  const anyReady = list.some((d) => d.ready);

  const send = useMutation({
    mutationFn: (req: SendRequest) => api.send(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["jobs"] });
      queryClient.invalidateQueries({ queryKey: ["stats"] });
    },
  });

  const created: Job[] = send.data ? ("jobs" in send.data ? send.data.jobs : [send.data]) : [];

  const tooMany = all.length > MAX_RECIPIENTS;
  const canSend =
    all.length > 0 &&
    invalid.length === 0 &&
    !tooMany &&
    body.trim() !== "" &&
    body.length <= MAX_BODY &&
    (route === "auto" || deviceId !== "") &&
    !send.isPending;
  const total = all.length * Math.max(segments.parts, 1);

  /** Turns what is typed into chips. */
  function commit(text = draft) {
    const parsed = parseRecipients(text);
    if (parsed.length > 0) setRecipients((r) => dedupe([...r, ...parsed]));
    setDraft("");
  }

  function onRecipientKey(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === "," || e.key === " " || e.key === ";") {
      if (draft.trim() !== "") {
        e.preventDefault();
        commit();
      } else if (e.key !== "Enter") {
        e.preventDefault();
      }
    } else if (e.key === "Backspace" && draft === "" && recipients.length > 0) {
      setRecipients((r) => r.slice(0, -1));
    }
  }

  function onRecipientPaste(e: ClipboardEvent<HTMLInputElement>) {
    const text = e.clipboardData.getData("text");
    if (/[\s,;]/.test(text)) {
      e.preventDefault();
      commit(draft + text);
    }
  }

  function submit(e?: FormEvent) {
    e?.preventDefault();
    if (!canSend) return;
    commit();
    const req: SendRequest = { body, mode };
    // The singular form when there is one number, so the reply is the plain
    // job object every other caller of POST /send gets.
    if (all.length === 1) req.recipient = all[0];
    else req.recipients = all;
    if (route === "device" && deviceId) {
      req.deviceId = deviceId;
      if (subscriptionId) req.subscriptionId = Number(subscriptionId);
    }
    if (priority) req.priority = priority;
    const at = localInputToISO(scheduledAt);
    if (at) req.scheduledAt = at;
    if (callbackUrl.trim()) req.callbackUrl = callbackUrl.trim();
    send.mutate(req);
  }

  function reset() {
    setRecipients([]);
    setDraft("");
    setBody("");
    send.reset();
  }

  const action = mode === "immediate" ? "Envoyer" : "Mettre en file";
  const sentBy =
    route === "auto"
      ? anyReady
        ? "Automatique → le meilleur appareil prêt"
        : "Automatique → en attente d'un appareil"
      : device
        ? `${device.label || shortId(device.deviceId)} · ${sim ? simLabel(sim) : "SIM par défaut"}`
        : "Choisissez un appareil";

  return (
    <>
      <PageHeader title="Envoyer un SMS" subtitle="Un message, un ou plusieurs destinataires — chacun devient un job distinct." />

      <div className="flex flex-wrap items-start gap-5">
        <form
          onSubmit={submit}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              submit();
            }
          }}
          className="card min-w-0 flex-[999_1_460px] overflow-hidden"
        >
          <Section>
            <div className="flex items-baseline justify-between">
              <label htmlFor="recipients" className="font-semibold">
                Destinataires
              </label>
              <span className={cn("font-mono text-xs text-muted-foreground", tooMany && "font-medium text-destructive")}>
                {all.length} / {MAX_RECIPIENTS}
              </span>
            </div>
            <div
              className="flex min-h-11 flex-wrap items-center gap-1.5 rounded-md border border-input bg-background/60 px-2 py-1.5 focus-within:border-primary focus-within:ring-[3px] focus-within:ring-ring/25"
              onClick={() => document.getElementById("recipients")?.focus()}
            >
              {recipients.map((r) => (
                <span
                  key={r}
                  className={cn(
                    "inline-flex h-7 items-center gap-1.5 rounded-md border bg-muted pr-1 pl-2.5 font-mono text-[13px]",
                    isE164(r) ? "border-input" : "border-danger-line bg-danger-soft text-destructive",
                  )}
                  title={isE164(r) ? undefined : "Numéro invalide : format E.164 attendu (+226…)"}
                >
                  {r}
                  <button
                    type="button"
                    aria-label={`Retirer ${r}`}
                    className="inline-flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                    onClick={(e) => {
                      e.stopPropagation();
                      setRecipients((list) => list.filter((x) => x !== r));
                    }}
                  >
                    <X className="size-3" aria-hidden />
                  </button>
                </span>
              ))}
              <input
                id="recipients"
                className="h-7 min-w-[200px] flex-1 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted-foreground"
                placeholder={recipients.length ? "" : "+226…, séparés par virgule, espace ou retour à la ligne"}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={onRecipientKey}
                onPaste={onRecipientPaste}
                onBlur={() => commit()}
                autoComplete="off"
              />
            </div>
            {invalid.length > 0 ? (
              <span className="text-xs text-destructive">Numéro(s) invalide(s) : {invalid.join(", ")}</span>
            ) : (
              <span className="text-xs text-muted-foreground">Format E.164. Collez une liste : les doublons sont retirés.</span>
            )}
          </Section>

          <Section>
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <label htmlFor="body" className="font-semibold">
                Message
              </label>
              <span className="inline-flex items-center gap-1.5">
                <span
                  className={cn(
                    "rounded px-1.5 font-mono text-[11px] font-medium",
                    segments.encoding === "GSM-7" ? "bg-success-soft text-success" : "bg-warning-soft text-warning",
                  )}
                >
                  {segments.encoding}
                </span>
                <span className="font-mono text-xs text-muted-foreground">
                  {segments.units} car. · {segments.parts} SMS · {segments.remaining} restants
                </span>
              </span>
            </div>
            <textarea
              id="body"
              rows={5}
              className="input min-h-32 resize-y py-2.5"
              value={body}
              maxLength={MAX_BODY}
              onChange={(e) => setBody(e.target.value)}
            />
            <div className="flex gap-[3px]" aria-hidden>
              <span className="h-[3px] rounded-sm bg-primary" style={{ flex: Math.max(segments.units, 0) }} />
              <span className="h-[3px] rounded-sm bg-muted" style={{ flex: Math.max(segments.remaining, 1) }} />
            </div>
            <span className="text-xs text-muted-foreground">
              {segments.encoding === "UCS-2"
                ? "Un caractère hors alphabet GSM (emoji, « ’ » typographique…) réduit chaque part à 70 caractères."
                : "Alphabet GSM : 160 caractères par SMS, 153 par part au-delà."}
            </span>
          </Section>

          <Section>
            <span className="font-semibold">Routage</span>
            <div className="grid gap-2.5 sm:grid-cols-2">
              <Choice checked={route === "auto"} onChange={() => setRoute("auto")} title="Automatique" text="Le meilleur appareil disponible, SIM par défaut." />
              <Choice checked={route === "device"} onChange={() => setRoute("device")} title="Appareil précis" text="Choisir le téléphone et la SIM." />
            </div>
            {route === "device" && (
              <div className="grid gap-2.5 sm:grid-cols-2">
                <label className="flex flex-col gap-1.5 text-[13px] font-medium">
                  Appareil
                  <select
                    className="input"
                    value={deviceId}
                    onChange={(e) => {
                      setDeviceId(e.target.value);
                      setSubscriptionId("");
                    }}
                  >
                    <option value="">Choisir…</option>
                    {list.map((d) => (
                      <option key={d.deviceId} value={d.deviceId} disabled={!d.enabled}>
                        {d.label || shortId(d.deviceId)} — {d.ready ? "prêt" : d.connected ? "connecté" : "hors ligne"}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="flex flex-col gap-1.5 text-[13px] font-medium">
                  SIM
                  <select className="input" value={subscriptionId} onChange={(e) => setSubscriptionId(e.target.value)} disabled={!device?.sims?.length}>
                    <option value="">SIM SMS par défaut</option>
                    {device?.sims?.map((s) => (
                      <option key={s.subscriptionId} value={s.subscriptionId}>
                        {simLabel(s)}
                        {s.isDefaultSms ? " (par défaut)" : ""}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            )}
            <div className="grid gap-2.5 sm:grid-cols-2">
              <Choice checked={mode === "queued"} onChange={() => setMode("queued")} title="En file" text="Attend qu'un appareil soit disponible et réessaie." />
              <Choice checked={mode === "immediate"} onChange={() => setMode("immediate")} title="Immédiat" text="Échoue tout de suite si aucun appareil n'est prêt (OTP)." />
            </div>

            <button
              type="button"
              className="inline-flex items-center gap-1 self-start text-sm font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground"
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
                  <input id="callback" type="url" className="input" placeholder="https://…" value={callbackUrl} onChange={(e) => setCallbackUrl(e.target.value)} />
                </div>
              </div>
            )}
          </Section>

          {send.error != null && (
            <div className="border-b px-5 py-3">
              <ErrorNote error={send.error} />
            </div>
          )}

          <div className="flex flex-wrap items-center gap-3 px-5 py-4">
            <span className="hidden text-[13px] text-muted-foreground sm:inline">
              Raccourci <kbd className="rounded border px-1.5 font-mono text-xs text-foreground">Ctrl ↵</kbd>
            </span>
            <div className="flex-1" />
            <button type="button" className="btn-secondary" onClick={reset}>
              Effacer
            </button>
            <button type="submit" className="btn-primary" disabled={!canSend}>
              {send.isPending ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Send className="size-4" aria-hidden />}
              {send.isPending ? "Envoi…" : `${action} ${total || ""} SMS`.replace("  ", " ")}
            </button>
          </div>
        </form>

        <div className="flex min-w-0 flex-[1_1_340px] flex-col gap-4">
          {created.length > 0 && (
            <section className="card overflow-hidden" aria-label="Résultat">
              <CardHeader title="Résultat" description="Pris en charge : le statut évolue dans l'historique." />
              <ul className="divide-y px-4 text-sm">
                {created.map((j) => (
                  <li key={j.jobId} className="flex items-center justify-between gap-2 py-2">
                    <span className="font-mono">{j.recipient}</span>
                    <StatusBadge status={j.status} />
                  </li>
                ))}
              </ul>
              <div className="flex gap-2 border-t p-3">
                <Link to="/history" className="btn-secondary">
                  <History className="size-4" aria-hidden />
                  Historique
                </Link>
                <button className="btn-secondary" onClick={reset}>
                  <SquarePen className="size-4" aria-hidden />
                  Nouveau
                </button>
              </div>
            </section>
          )}

          <section className="card overflow-hidden" aria-label="Aperçu">
            <div className="flex items-center justify-between border-b px-4 py-3">
              <span className="text-[13px] font-semibold">Aperçu</span>
              <span className="text-xs text-muted-foreground">tel que reçu</span>
            </div>
            <div className="flex flex-col gap-2.5 bg-background/60 px-4 pt-5 pb-6">
              <div className="flex flex-col items-center gap-0.5 pb-2">
                <span className="inline-flex size-9 items-center justify-center rounded-sm border bg-muted font-mono text-[11px] font-medium text-muted-foreground">SIM</span>
                <span className="font-mono text-xs text-muted-foreground">{sim?.phoneNumber || (sim ? simLabel(sim) : "Numéro de la SIM d'envoi")}</span>
              </div>
              <span className="max-w-[80%] self-start rounded-md rounded-bl-md bg-muted px-3.5 py-2.5 text-[15px] leading-snug break-words whitespace-pre-wrap">
                {body || <span className="text-muted-foreground">Votre message…</span>}
              </span>
              <span className="pl-1 text-[11px] text-muted-foreground">
                SMS · {Math.max(segments.parts, 1)}/{Math.max(segments.parts, 1)}
              </span>
            </div>
          </section>

          <section className="card overflow-hidden" aria-label="Estimation">
            <div className="border-b px-4 py-3 text-[13px] font-semibold">Estimation</div>
            <dl className="px-4 py-1.5 text-sm">
              <Row label="Destinataires">{all.length}</Row>
              <Row label="Parts / message">{segments.parts}</Row>
              <Row label="SMS au total" strong>
                {total}
              </Row>
              <Row label="Envoyé par" last mono={false}>
                {sentBy}
              </Row>
            </dl>
            {!anyReady && devices.isSuccess && (
              <div className="mx-3 mb-3 flex gap-2.5 rounded-md border border-warning-line bg-warning-soft px-3 py-2.5 text-[13px]">
                <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
                <span>
                  <span className="font-semibold">Aucun appareil prêt.</span>{" "}
                  <span className="text-muted-foreground">
                    {mode === "immediate"
                      ? "Un envoi immédiat échouera : passez en file ou reconnectez un téléphone."
                      : "Le message part en file et sera envoyé dès qu'un téléphone se reconnecte."}
                  </span>
                </span>
              </div>
            )}
          </section>
        </div>
      </div>
    </>
  );
}

function dedupe(list: string[]): string[] {
  return [...new Set(list)];
}

function simLabel(s: NonNullable<Device["sims"]>[number]): string {
  return `SIM ${s.slotIndex + 1} · ${s.carrier || s.displayName || "SIM"}`;
}

function Section({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-2 border-b px-5 py-5">{children}</div>;
}

function Row({ label, children, strong, last, mono = true }: { label: string; children: ReactNode; strong?: boolean; last?: boolean; mono?: boolean }) {
  return (
    <div className={cn("flex justify-between gap-4 py-2", !last && "border-b border-border/60")}>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn("m-0 text-right", mono && "font-mono", strong && "font-semibold")}>{children}</dd>
    </div>
  );
}

/** A radio drawn as a card: the design's routing choice. */
function Choice({ checked, onChange, title, text }: { checked: boolean; onChange: () => void; title: string; text: string }) {
  return (
    <label
      className={cn(
        "flex cursor-pointer gap-2.5 rounded-md border p-3 text-sm transition-colors",
        checked ? "border-primary bg-primary-soft shadow-[0_0_0_1px_var(--primary)]" : "border-input hover:bg-row-hover",
      )}
    >
      <input type="radio" className="sr-only" checked={checked} onChange={onChange} />
      <span
        aria-hidden
        className={cn("mt-0.5 size-4 shrink-0 rounded-full bg-card", checked ? "border-[5px] border-primary" : "border-[1.5px] border-input")}
      />
      <span className="flex flex-col gap-0.5">
        <span className="font-medium">{title}</span>
        <span className="text-xs text-muted-foreground">{text}</span>
      </span>
    </label>
  );
}
