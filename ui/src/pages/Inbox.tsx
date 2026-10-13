import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Inbox, Loader2, MessageSquareText, RefreshCw } from "lucide-react";
import { useSearchParams } from "react-router-dom";
import { api, type InboxQuery } from "../api/client";
import { Empty, ErrorNote, PageHeader, Pill } from "../components/common";
import { formatDateTime, shortId } from "../lib/format";

const PAGE_SIZE = 50;

export function InboxPage() {
  const [params, setParams] = useSearchParams();
  const deviceId = params.get("deviceId") ?? "";
  const sender = params.get("sender") ?? "";
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };

  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices });
  const deviceLabel = (id?: string) => {
    if (!id) return "—";
    const d = devices.data?.devices.find((x) => x.deviceId === id);
    return d?.label || shortId(id);
  };
  const forwarding = devices.data?.devices.filter((d) => d.policy.inbound?.enabled) ?? [];

  const filter: InboxQuery = { deviceId: deviceId || undefined, sender: sender || undefined, limit: PAGE_SIZE };
  // Refreshed by the live stream on every sms.received.
  const inbox = useInfiniteQuery({
    queryKey: ["inbox", filter],
    queryFn: ({ pageParam }) => api.listInbox({ ...filter, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const rows = inbox.data?.pages.flatMap((p) => p.messages) ?? [];

  return (
    <>
      <PageHeader
        title="SMS reçus"
        subtitle="Les messages que vos téléphones reçoivent et que leurs règles transmettent (ex. confirmations Orange Money)."
        actions={
          <button className="btn-secondary" onClick={() => inbox.refetch()} disabled={inbox.isFetching}>
            <RefreshCw className={inbox.isFetching ? "size-4 animate-spin" : "size-4"} aria-hidden />
            Actualiser
          </button>
        }
      />

      {devices.isSuccess && forwarding.length === 0 && (
        <div className="card mb-4 p-4 text-sm text-muted-foreground">
          Aucun téléphone ne transmet ses SMS reçus. Activez-le dans <span className="font-medium text-foreground">Appareils → Règles → SMS reçus</span>,
          en limitant de préférence aux expéditeurs utiles (OrangeMoney, MoovMoney…) : le reste ne quitte jamais le téléphone.
        </div>
      )}

      <div className="card mb-4 grid gap-3 p-4 sm:grid-cols-2">
        <select className="input" value={deviceId} onChange={(e) => setFilter("deviceId", e.target.value)} aria-label="Appareil">
          <option value="">Tous les appareils</option>
          {devices.data?.devices.map((d) => (
            <option key={d.deviceId} value={d.deviceId}>
              {d.label || shortId(d.deviceId)}
            </option>
          ))}
        </select>
        <input
          className="input"
          placeholder="Expéditeur exact (OrangeMoney)"
          value={sender}
          onChange={(e) => setFilter("sender", e.target.value.trim())}
          aria-label="Expéditeur"
        />
      </div>

      <ErrorNote error={inbox.error} />

      <div className="card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="border-b bg-muted/50">
              <tr>
                <th className="th">Reçu</th>
                <th className="th">Expéditeur</th>
                <th className="th">Message</th>
                <th className="th">Appareil</th>
                <th className="th">Webhook</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((m) => (
                <tr key={m.messageId} className="align-top">
                  <td className="td whitespace-nowrap text-muted-foreground">{formatDateTime(m.receivedAt)}</td>
                  <td className="td whitespace-nowrap font-medium">{m.sender}</td>
                  <td className="td max-w-xl text-sm whitespace-pre-wrap">{m.body}</td>
                  <td className="td whitespace-nowrap text-muted-foreground">
                    {deviceLabel(m.deviceId)}
                    {m.subscriptionId !== undefined && <span className="text-xs"> · SIM {m.subscriptionId}</span>}
                  </td>
                  <td className="td whitespace-nowrap">
                    {!m.webhook ? (
                      <span className="text-xs text-muted-foreground">—</span>
                    ) : m.webhook.delivered ? (
                      <Pill tone="success">Livré</Pill>
                    ) : m.webhook.lastError ? (
                      <span title={m.webhook.lastError}>
                        <Pill tone="warning">{m.webhook.attempts} essai(s)</Pill>
                      </span>
                    ) : (
                      <Pill tone="info">En cours</Pill>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {inbox.isSuccess && rows.length === 0 && (
          <Empty icon={sender || deviceId ? <Inbox /> : <MessageSquareText />}>
            {sender || deviceId ? "Aucun message ne correspond aux filtres." : "Aucun SMS reçu pour l'instant."}
          </Empty>
        )}
        {inbox.isLoading && <Empty icon={<Loader2 className="animate-spin" />}>Chargement…</Empty>}
      </div>

      {inbox.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <button className="btn-secondary" onClick={() => inbox.fetchNextPage()} disabled={inbox.isFetchingNextPage}>
            {inbox.isFetchingNextPage ? "Chargement…" : "Charger plus"}
          </button>
        </div>
      )}
    </>
  );
}
