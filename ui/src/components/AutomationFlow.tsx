import { ArrowDown, Filter, Globe, Hash, MessageSquareReply, MessageSquareText, Regex, Send, type LucideIcon } from "lucide-react";
import { Fragment, type ReactNode } from "react";
import type { Automation, AutomationActionType, Device } from "../api/types";
import { fieldsOf } from "../lib/automation";
import { cn } from "@/lib/utils";

const OP_LABEL: Record<string, string> = {
  eq: "=",
  ne: "≠",
  contains: "contient",
  gt: ">",
  gte: "≥",
  lt: "<",
  lte: "≤",
};

const ACTION: Record<AutomationActionType, { icon: LucideIcon; label: string }> = {
  forward: { icon: Send, label: "Transmettre à la passerelle" },
  reply_sms: { icon: MessageSquareReply, label: "Répondre par SMS" },
  webhook: { icon: Globe, label: "Webhook depuis le téléphone" },
  ussd: { icon: Hash, label: "Code USSD" },
};

/**
 * An automation drawn as what it is: a flow, from the SMS that arrives to the
 * actions it sets off, each step saying what it holds. Read-only; the editor
 * changes it.
 */
export function AutomationFlow({ automation: a, device }: { automation: Automation; device: Device }) {
  const fields = fieldsOf(a.pattern);
  const sim = a.subscriptionId ? device.sims?.find((s) => s.subscriptionId === a.subscriptionId) : undefined;
  const limits = [a.limits?.perHour && `${a.limits.perHour} / h`, a.limits?.perDay && `${a.limits.perDay} / jour`].filter(Boolean).join(" · ");

  return (
    <ol className="flex flex-col items-stretch" aria-label="Déroulé de l'automatisation">
      <Step icon={MessageSquareText} kind="Déclencheur" title="Un SMS arrive">
        <Facts
          rows={[
            ["Expéditeur", a.senders?.length ? a.senders.join(", ") : "n'importe lequel"],
            ["SIM", sim ? `SIM ${sim.slotIndex + 1}${sim.carrier ? ` · ${sim.carrier}` : ""}` : "toutes"],
            ...(limits ? [["Plafond", limits] as [string, string]] : []),
          ]}
        />
      </Step>

      {a.pattern && (
        <>
          <Link />
          <Step icon={Regex} kind="Extraction" title="Le texte correspond au motif">
            <code className="block rounded-sm bg-muted px-2 py-1.5 font-mono text-xs break-all">{a.pattern}</code>
            {fields.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {fields.map((f) => (
                  <span key={f} className="rounded-sm border px-1.5 py-0.5 font-mono text-[11px]">
                    {`{${f}}`}
                  </span>
                ))}
              </div>
            )}
          </Step>
        </>
      )}

      {a.conditions && a.conditions.length > 0 && (
        <>
          <Link />
          <Step icon={Filter} kind="Conditions" title={a.conditions.length > 1 ? "Toutes doivent être vraies" : "Elle doit être vraie"}>
            <ul className="space-y-1 font-mono text-xs">
              {a.conditions.map((c, i) => (
                <li key={i}>
                  {`{${c.field}}`} <span className="text-muted-foreground">{OP_LABEL[c.op] ?? c.op}</span> {c.value}
                </li>
              ))}
            </ul>
          </Step>
        </>
      )}

      {a.actions.map((act, i) => {
        const { icon, label } = ACTION[act.type];
        const detail =
          act.type === "reply_sms" ? `à ${act.to} : « ${act.text} »` : act.type === "webhook" ? act.url : act.type === "ussd" ? act.code : "Dans les SMS reçus et vers le webhook du compte";
        return (
          <Fragment key={i}>
            <Link label={i > 0 ? "puis" : undefined} />
            <Step icon={icon} kind={`Action ${i + 1}`} title={label} accent>
              {detail && <p className={cn("text-xs break-all", act.type === "forward" ? "text-muted-foreground" : "font-mono")}>{detail}</p>}
            </Step>
          </Fragment>
        );
      })}
    </ol>
  );
}

function Step({ icon: Icon, kind, title, accent, children }: { icon: LucideIcon; kind: string; title: string; accent?: boolean; children?: ReactNode }) {
  return (
    <li className={cn("list-none rounded-md border bg-card p-3", accent && "border-l-[3px] border-l-foreground")}>
      <div className="flex items-center gap-2">
        <span className="inline-flex size-7 shrink-0 items-center justify-center border border-input" aria-hidden>
          <Icon className="size-3.5" />
        </span>
        <div className="min-w-0">
          <div className="eyebrow">{kind}</div>
          <div className="text-sm font-semibold">{title}</div>
        </div>
      </div>
      {children && <div className="mt-2.5 pl-9">{children}</div>}
    </li>
  );
}

/** The arrow between two steps. */
function Link({ label }: { label?: string }) {
  return (
    <li className="flex list-none flex-col items-center py-1 text-muted-foreground" aria-hidden>
      <span className="h-3 w-px bg-border" />
      {label && <span className="font-mono text-[10px] uppercase">{label}</span>}
      <ArrowDown className="size-3.5" />
    </li>
  );
}

function Facts({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="m-0 font-mono">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
