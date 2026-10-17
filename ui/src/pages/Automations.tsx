import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, FlaskConical, Loader2, Pencil, Plus, RefreshCw, Trash2, Workflow, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { api } from "../api/client";
import type { Automation, AutomationAction, AutomationActionType, AutomationCondition, Device, Policy } from "../api/types";
import { CardHeader, ConfirmDialog, Empty, ErrorNote, Modal, ModalActions, PageHeader, Pill } from "../components/common";
import { formatDateTime } from "../lib/format";
import { Switch } from "@/components/ui/switch";
import { fieldsOf, jsonBodyError, match, numbersOf, patternError, render, renderBody, renderUssd, unknownFields } from "../lib/automation";
import { shortId } from "../lib/format";
import { cn } from "@/lib/utils";

const ACTION_LABEL: Record<AutomationActionType, string> = {
  forward: "Transmettre à la passerelle",
  reply_sms: "Répondre par SMS",
  webhook: "Webhook depuis le téléphone",
  ussd: "Lancer un code USSD",
};

/** The capability an agent announces for each action. */
const ACTION_CAPABILITY: Record<AutomationActionType, string> = {
  forward: "automations",
  reply_sms: "automation_reply_sms",
  webhook: "automation_webhook",
  ussd: "automation_ussd",
};

const OPS = [
  { op: "eq", label: "=" },
  { op: "ne", label: "≠" },
  { op: "contains", label: "contient" },
  { op: "gt", label: ">" },
  { op: "gte", label: "≥" },
  { op: "lt", label: "<" },
  { op: "lte", label: "≤" },
];

const blank = (): Automation => ({
  id: "",
  name: "",
  enabled: true,
  senders: [],
  pattern: "",
  conditions: [],
  actions: [{ type: "forward" }],
  limits: {},
});

export function AutomationsPage() {
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });
  const list = devices.data?.devices ?? [];
  const deviceId = params.get("deviceId") ?? list[0]?.deviceId ?? "";
  const device = list.find((d) => d.deviceId === deviceId);
  const automations = device?.policy.automations ?? [];

  const [editing, setEditing] = useState<Automation | null>(null);
  const [removing, setRemoving] = useState<Automation | null>(null);

  const save = useMutation({
    mutationFn: (next: Automation[]) => {
      const p = device!.policy;
      const rest: Omit<Policy, "version" | "updatedAt"> = {
        availability: p.availability,
        accept: p.accept,
        sims: p.sims,
        limits: p.limits,
        inbound: p.inbound,
        automations: next,
      };
      return api.putPolicy(device!.deviceId, rest);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["devices"] });
      toast.success("Automatisations enregistrées", {
        description: device?.connected ? "Transmises au téléphone." : "Le téléphone les recevra à sa reconnexion.",
      });
      setEditing(null);
      setRemoving(null);
    },
  });

  const upsert = (a: Automation) =>
    save.mutate(a.id ? automations.map((x) => (x.id === a.id ? a : x)) : [...automations, a]);
  const toggle = (a: Automation, enabled: boolean) => save.mutate(automations.map((x) => (x.id === a.id ? { ...x, enabled } : x)));

  return (
    <>
      <PageHeader
        title="Automatisations"
        subtitle="Quand un SMS arrive, le téléphone en extrait des champs et agit lui-même — même hors ligne."
        actions={
          device && (
            <button className="btn-primary" onClick={() => setEditing(blank())}>
              <Plus className="size-4" aria-hidden />
              Nouvelle
            </button>
          )
        }
      />

      <div className="card mb-4 p-4">
        <select
          className="input sm:max-w-sm"
          value={deviceId}
          onChange={(e) => setParams({ deviceId: e.target.value }, { replace: true })}
          aria-label="Appareil"
        >
          {list.map((d) => (
            <option key={d.deviceId} value={d.deviceId}>
              {d.label || shortId(d.deviceId)}
            </option>
          ))}
        </select>
        {device && !device.capabilities.includes("automations") && (
          <p className="mt-2 text-xs text-muted-foreground">
            L'application de ce téléphone ne sait pas encore exécuter d'automatisations : elles seront enregistrées, et s'appliqueront
            une fois l'app mise à jour.
          </p>
        )}
      </div>

      <ErrorNote error={devices.error ?? save.error} />

      {devices.isSuccess && list.length === 0 && <Empty icon={<Workflow />}>Aucun téléphone enrôlé.</Empty>}

      <div className="grid gap-4 lg:grid-cols-2">
        {automations.map((a) => (
          <div key={a.id} className="card overflow-hidden">
            <CardHeader
              title={
                <span className="flex items-center gap-2">
                  {a.name}
                  {!a.enabled && <Pill tone="neutral">désactivée</Pill>}
                </span>
              }
              description={summary(a, device!)}
              actions={
                <div className="flex items-center gap-2">
                  <Switch checked={!!a.enabled} onCheckedChange={(on) => toggle(a, on)} aria-label="Activer" />
                  <button className="btn-secondary" onClick={() => setEditing(a)} aria-label="Modifier">
                    <Pencil className="size-4" aria-hidden />
                  </button>
                  <button className="btn-secondary" onClick={() => setRemoving(a)} aria-label="Supprimer">
                    <Trash2 className="size-4" aria-hidden />
                  </button>
                </div>
              }
            />
            <ul className="space-y-1 p-4 text-sm">
              {a.actions.map((act, i) => (
                <li key={i} className="flex items-center gap-2">
                  <span className="text-muted-foreground">{i + 1}.</span> {ACTION_LABEL[act.type]}
                  {act.type === "reply_sms" && <span className="truncate font-mono text-xs text-muted-foreground">→ {act.to}</span>}
                  {act.type === "ussd" && <span className="truncate font-mono text-xs text-muted-foreground">{act.code}</span>}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      {device && automations.length === 0 && (
        <div className="card">
          <Empty icon={<Workflow />}>
            Aucune automatisation sur ce téléphone. Exemple : sur « Vous avez reçu … FCFA » d'OrangeMoney, extraire le montant et la
            référence, et les transmettre à votre système.
          </Empty>
        </div>
      )}

      {device && <RunLog deviceId={device.deviceId} names={Object.fromEntries(automations.map((a) => [a.id, a.name]))} />}

      {editing && device && (
        <AutomationEditor
          device={device}
          initial={editing}
          busy={save.isPending}
          error={save.error}
          onSave={upsert}
          onClose={() => setEditing(null)}
        />
      )}
      {removing && (
        <ConfirmDialog
          title={`Supprimer « ${removing.name} » ?`}
          body="Le téléphone cessera de l'exécuter dès qu'il aura reçu ses nouvelles règles."
          confirmLabel="Supprimer"
          busy={save.isPending}
          onConfirm={() => save.mutate(automations.filter((x) => x.id !== removing.id))}
          onCancel={() => setRemoving(null)}
        />
      )}
    </>
  );
}

function summary(a: Automation, device: Device): string {
  const from = a.senders?.length ? a.senders.join(", ") : "tout expéditeur";
  const sim = a.subscriptionId ? `SIM ${device.sims?.find((s) => s.subscriptionId === a.subscriptionId)?.slotIndex ?? "?"}` : "toutes SIM";
  return `${from} · ${sim}${a.pattern ? " · motif" : ""}`;
}

function AutomationEditor({
  device,
  initial,
  busy,
  error,
  onSave,
  onClose,
}: {
  device: Device;
  initial: Automation;
  busy: boolean;
  error: unknown;
  onSave: (a: Automation) => void;
  onClose: () => void;
}) {
  const [a, setA] = useState<Automation>(initial);
  const [sendersRaw, setSendersRaw] = useState((initial.senders ?? []).join(", "));
  const set = (patch: Partial<Automation>) => setA((x) => ({ ...x, ...patch }));
  const fields = useMemo(() => fieldsOf(a.pattern), [a.pattern]);

  const problems: string[] = [];
  if (!a.name.trim()) problems.push("Donnez un nom.");
  const pe = patternError(a.pattern);
  if (pe) problems.push(`Motif : ${pe}`);
  if (a.actions.length === 0) problems.push("Ajoutez au moins une action.");
  a.actions.forEach((act, i) => {
    const unknown = [
      ...unknownFields(act.to, fields),
      ...unknownFields(act.text, fields),
      ...unknownFields(act.body, fields),
      ...unknownFields(act.code, fields, true),
    ];
    if (unknown.length) problems.push(`Action ${i + 1} : champ(s) inconnu(s) ${unknown.map((u) => `{${u}}`).join(", ")}`);
    if (act.type === "reply_sms" && (!act.to?.trim() || !act.text?.trim())) problems.push(`Action ${i + 1} : destinataire et texte requis.`);
    if (act.type === "webhook" && !/^https?:\/\/[^/]+/.test(act.url ?? "")) problems.push(`Action ${i + 1} : URL http(s) requise.`);
    const bodyError = act.type === "webhook" ? jsonBodyError(act.body, fields) : null;
    if (bodyError) problems.push(`Action ${i + 1} : ${bodyError}`);
    if (act.type === "ussd" && !/^[*#]([0-9*#+]|\{[A-Za-z][A-Za-z0-9]*\})*#$/.test(act.code ?? "")) {
      problems.push(`Action ${i + 1} : code USSD invalide (ex. *144*2*1*{from}*{amount}*{pin}#).`);
    }
    if (!device.capabilities.includes(ACTION_CAPABILITY[act.type])) {
      problems.push(`Action ${i + 1} : l'app de ce téléphone ne sait pas encore « ${ACTION_LABEL[act.type]} » (mise à jour nécessaire).`);
    }
  });
  (a.conditions ?? []).forEach((c, i) => {
    if (!fields.includes(c.field)) problems.push(`Condition ${i + 1} : champ inconnu.`);
  });
  const blocking = problems.filter((p) => !p.includes("mise à jour nécessaire"));

  const setAction = (i: number, patch: Partial<AutomationAction>) =>
    set({
      actions: a.actions.map((x, j) => {
        if (j !== i) return x;
        const next = { ...x, ...patch };
        // A webhook is signed from the start: its secret is made here, not typed.
        if (next.type === "webhook" && !next.secret) next.secret = webhookSecret();
        return next;
      }),
    });
  const setCondition = (i: number, patch: Partial<AutomationCondition>) =>
    set({ conditions: (a.conditions ?? []).map((x, j) => (j === i ? { ...x, ...patch } : x)) });

  return (
    <Modal
      onClose={onClose}
      wide
      label={initial.id ? "Modifier l'automatisation" : "Nouvelle automatisation"}
      description="Ce que le téléphone fait seul quand un SMS arrive."
    >
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label className="label" htmlFor="auto-name">
              Nom
            </label>
            <input id="auto-name" className="input" value={a.name} onChange={(e) => set({ name: e.target.value })} placeholder="Paiement reçu" />
          </div>
          <div>
            <label className="label" htmlFor="auto-sim">
              SIM écoutée
            </label>
            <select
              id="auto-sim"
              className="input"
              value={a.subscriptionId ?? 0}
              onChange={(e) => set({ subscriptionId: Number(e.target.value) || undefined })}
            >
              <option value={0}>Toutes les SIM</option>
              {device.sims?.map((s) => (
                <option key={s.subscriptionId} value={s.subscriptionId}>
                  Slot {s.slotIndex + 1} — {s.carrier || s.displayName || "SIM"}
                </option>
              ))}
            </select>
          </div>
        </div>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold">Déclencheur</h3>
          <div>
            <label className="label" htmlFor="auto-senders">
              Expéditeurs
            </label>
            <input
              id="auto-senders"
              className="input"
              placeholder="OrangeMoney, MoovMoney — vide : tous"
              value={sendersRaw}
              onChange={(e) => {
                setSendersRaw(e.target.value);
                set({ senders: e.target.value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean) });
              }}
            />
          </div>
          <div>
            <label className="label" htmlFor="auto-pattern">
              Motif <span className="font-normal text-muted-foreground">(groupes nommés = champs)</span>
            </label>
            <input
              id="auto-pattern"
              className="input font-mono"
              placeholder="recu (?<amount>[0-9 ]+) FCFA de (?<from>[0-9]+)"
              value={a.pattern ?? ""}
              onChange={(e) => set({ pattern: e.target.value })}
            />
            <p className="mt-1 text-xs text-muted-foreground">
              Champs disponibles :{" "}
              {fields.map((f) => (
                <code key={f} className="mr-1 rounded bg-muted px-1">{`{${f}}`}</code>
              ))}
            </p>
          </div>
          <div className="space-y-2">
            {(a.conditions ?? []).map((c, i) => (
              <div key={i} className="flex gap-2">
                <select className="input" value={c.field} onChange={(e) => setCondition(i, { field: e.target.value })} aria-label="Champ">
                  {fields.map((f) => (
                    <option key={f}>{f}</option>
                  ))}
                </select>
                <select className="input w-32" value={c.op} onChange={(e) => setCondition(i, { op: e.target.value })} aria-label="Opérateur">
                  {OPS.map((o) => (
                    <option key={o.op} value={o.op}>
                      {o.label}
                    </option>
                  ))}
                </select>
                <input className="input" value={c.value} onChange={(e) => setCondition(i, { value: e.target.value })} aria-label="Valeur" />
                <button
                  className="btn-secondary"
                  onClick={() => set({ conditions: (a.conditions ?? []).filter((_, j) => j !== i) })}
                  aria-label="Retirer la condition"
                >
                  <X className="size-4" aria-hidden />
                </button>
              </div>
            ))}
            <button
              type="button"
              className="text-sm font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground"
              onClick={() => set({ conditions: [...(a.conditions ?? []), { field: fields.at(-1) ?? "body", op: "gte", value: "" }] })}
            >
              + Condition
            </button>
          </div>
        </section>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold">Actions (dans l'ordre)</h3>
          {a.actions.map((act, i) => (
            <div key={i} className="space-y-2 rounded-md border p-3">
              <div className="flex gap-2">
                <select
                  className="input"
                  value={act.type}
                  onChange={(e) => setAction(i, { type: e.target.value as AutomationActionType })}
                  aria-label="Type d'action"
                >
                  {(Object.keys(ACTION_LABEL) as AutomationActionType[]).map((t) => (
                    <option key={t} value={t}>
                      {ACTION_LABEL[t]}
                    </option>
                  ))}
                </select>
                <button className="btn-secondary" onClick={() => set({ actions: a.actions.filter((_, j) => j !== i) })} aria-label="Retirer l'action">
                  <X className="size-4" aria-hidden />
                </button>
              </div>
              {act.type === "forward" && (
                <p className="text-xs text-muted-foreground">Le SMS et ses champs apparaissent dans « Reçus » et partent vers le webhook du compte.</p>
              )}
              {act.type === "reply_sms" && (
                <>
                  <input className="input font-mono" placeholder="{sender} ou +22670000000" value={act.to ?? ""} onChange={(e) => setAction(i, { to: e.target.value })} />
                  <textarea className="input min-h-16" placeholder="Merci, {amount} FCFA bien reçus." value={act.text ?? ""} onChange={(e) => setAction(i, { text: e.target.value })} />
                </>
              )}
              {act.type === "webhook" && (
                <>
                  <input className="input" type="url" placeholder="https://…" value={act.url ?? ""} onChange={(e) => setAction(i, { url: e.target.value })} />
                  <textarea
                    className="input min-h-16 font-mono"
                    placeholder='Corps (facultatif) — vide : {"event": "automation.triggered", "data": {le SMS et ses champs}}. Ex. {"montant": {amount}, "de": "{from}", "ref": "{ref}"}'
                    value={act.body ?? ""}
                    onChange={(e) => setAction(i, { body: e.target.value })}
                  />
                  <p className="text-xs text-muted-foreground">
                    En JSON, un champ entre guillemets (<code>"{"{from}"}"</code>) arrive en texte, échappé ; sans guillemets (<code>{"{amount}"}</code>) en
                    nombre — « 5 000 » devient 5000 —, ou <code>null</code> s'il est vide. Chaque envoi porte l'en-tête{" "}
                    <code>X-Relaix-Delivery</code>, le même à chaque nouvel essai : ignorez un id déjà traité.
                  </p>
                  <SigningSecret secret={act.secret ?? ""} onRegenerate={() => setAction(i, { secret: webhookSecret() })} />
                </>
              )}
              {act.type === "ussd" && (
                <>
                  <input className="input font-mono" placeholder="*144*2*1*{from}*{amount}*{pin}#" value={act.code ?? ""} onChange={(e) => setAction(i, { code: e.target.value })} />
                  <p className="text-xs text-muted-foreground">
                    <code>{"{pin}"}</code> vient du coffre du téléphone et n'en sort jamais. Le code ne s'exécute qu'après approbation sur le
                    téléphone, dans les plafonds fixés là-bas.
                  </p>
                </>
              )}
            </div>
          ))}
          <button
            type="button"
            className="text-sm font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground"
            onClick={() => set({ actions: [...a.actions, { type: "forward" }] })}
          >
            + Action
          </button>
        </section>

        <section className="grid gap-4 sm:grid-cols-2">
          <div>
            <label className="label" htmlFor="auto-hour">
              Max. par heure
            </label>
            <input
              id="auto-hour"
              type="number"
              min={0}
              className="input"
              value={a.limits?.perHour ?? ""}
              onChange={(e) => set({ limits: { ...a.limits, perHour: Number(e.target.value) || undefined } })}
            />
          </div>
          <div>
            <label className="label" htmlFor="auto-day">
              Max. par jour
            </label>
            <input
              id="auto-day"
              type="number"
              min={0}
              className="input"
              value={a.limits?.perDay ?? ""}
              onChange={(e) => set({ limits: { ...a.limits, perDay: Number(e.target.value) || undefined } })}
            />
          </div>
        </section>

        <Tester automation={a} />

        <label className="flex items-center justify-between gap-4 rounded-md border p-3 text-sm">
          <span className="font-medium">Activée</span>
          <Switch checked={!!a.enabled} onCheckedChange={(enabled) => set({ enabled })} />
        </label>

        {problems.length > 0 && (
          <ul className="space-y-1 rounded-md border border-warning-line bg-warning-soft p-3 text-xs">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        <ErrorNote error={error} />
      </div>

      <ModalActions>
        <button className="btn-secondary" onClick={onClose}>
          Annuler
        </button>
        <button className="btn-primary" disabled={busy || blocking.length > 0} onClick={() => onSave({ ...a, name: a.name.trim() })}>
          {busy ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Check className="size-4" aria-hidden />}
          Enregistrer
        </button>
      </ModalActions>
    </Modal>
  );
}

/** Paste a message, see what the phone would do with it. */
function Tester({ automation }: { automation: Automation }) {
  const [sender, setSender] = useState("OrangeMoney");
  const [body, setBody] = useState("");
  const result = body ? match({ ...automation, enabled: true }, { sender, body, receivedAt: new Date().toISOString(), sim: automation.subscriptionId ?? 0 }) : null;
  const extracted = result ? Object.entries(result.fields).filter(([k]) => !["sender", "body", "receivedAt", "sim"].includes(k)) : [];
  const numbers = result ? numbersOf(result.fields) : {};

  return (
    <section className="space-y-2 rounded-md border bg-muted/40 p-3">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <FlaskConical className="size-4" aria-hidden /> Tester avec un SMS
      </h3>
      <input className="input" value={sender} onChange={(e) => setSender(e.target.value)} placeholder="Expéditeur" aria-label="Expéditeur du test" />
      <textarea
        className="input min-h-16"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder="Collez ici un SMS reçu, ex. « Vous avez recu 5 000 FCFA de 70123456. Ref: CI230512.1234 »"
        aria-label="Message de test"
      />
      {result && (
        <div className="space-y-2 text-sm">
          <Pill tone={result.matched ? "success" : "neutral"}>{result.matched ? "Déclenche" : `Ne déclenche pas : ${result.reason}`}</Pill>
          {extracted.length > 0 && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-xs">
              {extracted.map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd>
                    {v}
                    {k in numbers && String(numbers[k]) !== v && <span className="text-muted-foreground"> → {numbers[k]}</span>}
                  </dd>
                </div>
              ))}
            </dl>
          )}
          {result.matched && (
            <ol className="list-decimal space-y-1 pl-5 text-xs">
              {automation.actions.map((act, i) => (
                <li key={i} className={cn(act.type === "ussd" && "font-mono")}>
                  {act.type === "forward" && "Transmis à la passerelle avec ces champs"}
                  {act.type === "reply_sms" && `SMS à ${render(act.to ?? "", result.fields)} : « ${render(act.text ?? "", result.fields)} »`}
                  {act.type === "webhook" && `POST ${act.url}${act.body ? ` — ${renderBody(act.body, result.fields)}` : ""}`}
                  {act.type === "ussd" && renderUssd(act.code ?? "", result.fields)}
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </section>
  );
}

/** What the phone's automations did, newest first. */
function RunLog({ deviceId, names }: { deviceId: string; names: Record<string, string> }) {
  const runs = useQuery({ queryKey: ["runs", deviceId], queryFn: () => api.listRuns(deviceId) });
  const rows = runs.data?.runs ?? [];
  return (
    <div className="card mt-6 overflow-hidden">
      <CardHeader title="Dernières exécutions" description="Rapportées par le téléphone une fois terminées — jamais le PIN." />
      <ErrorNote error={runs.error} />
      {runs.isSuccess && rows.length === 0 ? (
        <Empty icon={<Workflow />}>Aucune exécution pour l'instant.</Empty>
      ) : (
        <ul className="divide-y">
          {rows.map((r) => (
            <li key={r.runId} className="space-y-1 p-4 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <Pill tone={r.ok ? "success" : "danger"}>{r.ok ? "Réussie" : "Échouée"}</Pill>
                <span className="font-medium">{names[r.automationId] ?? r.automationId}</span>
                {r.sender && <span className="text-muted-foreground">· {r.sender}</span>}
                <span className="ml-auto text-xs text-muted-foreground">{formatDateTime(r.startedAt)}</span>
              </div>
              {r.error && <p className="text-xs text-destructive">{r.error}</p>}
              <ol className="list-decimal space-y-0.5 pl-5 text-xs text-muted-foreground">
                {r.steps.map((s, i) => (
                  <li key={i} className={cn(!s.ok && "text-destructive")}>
                    {ACTION_LABEL[s.action] ?? s.action}
                    {s.detail && <span className="font-mono"> — {s.detail}</span>}
                    {s.responses?.map((resp, j) => (
                      <div key={j} className="mt-0.5 rounded bg-muted px-2 py-1 font-mono text-foreground">
                        {resp}
                      </div>
                    ))}
                  </li>
                ))}
              </ol>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** A new signing secret: 32 random bytes, base64url, behind a prefix that says what it is. */
function webhookSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return "whsec_" + btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** The webhook's signing secret: generated, shown to be copied into the receiver, never typed. */
function SigningSecret({ secret, onRegenerate }: { secret: string; onRegenerate: () => void }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  return (
    <div className="space-y-1">
      <span className="label">Secret de signature</span>
      <div className="flex gap-2">
        <input className="input font-mono text-xs" value={secret} readOnly onFocus={(e) => e.target.select()} aria-label="Secret de signature" />
        <button
          type="button"
          className="btn-secondary"
          // Unavailable off a secure origin (plain http to a LAN address): the field still selects for a manual copy.
          onClick={() => navigator.clipboard?.writeText(secret).then(() => setCopied(true), () => {})}
          aria-label="Copier le secret"
          title="Copier"
        >
          {copied ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
        </button>
        <button type="button" className="btn-secondary" onClick={onRegenerate} aria-label="Générer un nouveau secret" title="Générer un nouveau secret">
          <RefreshCw className="size-4" aria-hidden />
        </button>
      </div>
      <p className="text-xs text-muted-foreground">
        À copier dans le récepteur pour vérifier <code>X-Relaix-Signature</code> (voir Intégration). Un nouveau secret ne compte
        qu'une fois l'automatisation enregistrée.
      </p>
    </div>
  );
}
