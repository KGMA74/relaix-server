import { Check, Copy } from "lucide-react";
import { useState, type ReactNode } from "react";
import { PageHeader } from "../components/common";

export function IntegrationPage() {
  // The API is served by the same gatewayd as this page, so its address is
  // this page's origin.
  const base = window.location.origin;

  return (
    <>
      <PageHeader title="Intégration" subtitle="Envoyer des SMS depuis vos applications avec l'API REST de la passerelle." />

      <div className="space-y-6">
        <Section title="Authentification">
          <p>
            Chaque requête porte la clé API du serveur (<code>RELAIX_API_KEY</code>) en en-tête <code>Authorization</code>. Les erreurs ont
            toujours la forme <code>{`{"error": {"code", "message"}}`}</code> : le <code>code</code> est stable, le message peut changer.
          </p>
          <Code>{`Authorization: Bearer $RELAIX_API_KEY`}</Code>
        </Section>

        <Section title="Envoyer un SMS — POST /send">
          <p>
            Répond <code>202 Accepted</code> : la passerelle a pris le message en charge, pas encore envoyé. <code>mode</code> vaut{" "}
            <code>queued</code> (par défaut, attend un appareil) ou <code>immediate</code> (<code>503</code> si aucun appareil n'est prêt).
          </p>
          <Code>{`curl -X POST ${base}/send \\
  -H "Authorization: Bearer $RELAIX_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "recipient": "+237670000000",
    "body": "Votre code est 482913",
    "mode": "immediate",
    "callbackUrl": "https://example.com/hooks/sms"
  }'`}</Code>
          <Table
            rows={[
              ["recipient", "E.164. Ou recipients: [...] (jusqu'à 100) — un job par numéro, réponse {jobs: [...]}."],
              ["body", "Texte du message, 1530 caractères au plus."],
              ["deviceId", "Optionnel : impose un téléphone. Jamais remplacé par un autre."],
              ["subscriptionId", "Optionnel, avec deviceId : la SIM d'un téléphone multi-SIM (voir GET /devices)."],
              ["mode", "queued | immediate."],
              ["priority", "Entier, plus haut = traité d'abord."],
              ["scheduledAt", "RFC 3339 : envoi différé."],
              ["expiresAt", "RFC 3339 : au-delà, le message n'est plus envoyé (OTP périmé)."],
              ["callbackUrl", "Webhook appelé quand le job atteint un état final."],
            ]}
          />
        </Section>

        <Section title="Suivre un message">
          <Code>{`# Un job
curl ${base}/jobs/$JOB_ID -H "Authorization: Bearer $RELAIX_API_KEY"

# Sa chronologie
curl ${base}/jobs/$JOB_ID/events -H "Authorization: Bearer $RELAIX_API_KEY"

# Annuler tant qu'il n'est pas parti
curl -X DELETE ${base}/jobs/$JOB_ID -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
          <p>
            Statuts : <code>pending</code> → <code>assigned</code> → <code>sent</code> → <code>delivered</code> (si l'opérateur renvoie un
            accusé), ou <code>failed</code> / <code>cancelled</code>.
          </p>
        </Section>

        <Section title="Historique — GET /jobs">
          <p>
            Du plus récent au plus ancien, paginé par curseur : passez le <code>nextCursor</code> de la réponse en <code>cursor</code> tant
            qu'il est présent. Filtres combinables : <code>status</code> (liste séparée par des virgules), <code>deviceId</code>,{" "}
            <code>recipient</code> (préfixe), <code>from</code> / <code>to</code> (RFC 3339), <code>limit</code> (200 max).
          </p>
          <Code>{`curl "${base}/jobs?status=failed&recipient=%2B237&limit=50" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
        </Section>

        <Section title="Webhooks">
          <p>
            Un <code>POST</code> JSON vers <code>callbackUrl</code> à chaque état final, réessayé avec un délai croissant tant que votre
            serveur ne répond pas <code>2xx</code>. Vérifiez la signature avant de lui faire confiance : HMAC-SHA256 de{" "}
            <code>timestamp + "." + corps brut</code> avec le secret <code>RELAIX_CALLBACK_SECRET</code>.
          </p>
          <Code>{`X-Relaix-Timestamp: 1767225600
X-Relaix-Signature: sha256=<hex>
X-Relaix-Delivery: <jobId>

{"jobId":"…","status":"sent","recipient":"+237670000000","partsSent":1,"attempt":1}`}</Code>
          <Code>{`// Node.js
import crypto from "node:crypto";

function verify(secret, timestamp, rawBody, header) {
  const mac = crypto.createHmac("sha256", secret)
    .update(timestamp + ".").update(rawBody).digest("hex");
  const expected = Buffer.from("sha256=" + mac);
  const got = Buffer.from(header ?? "");
  return got.length === expected.length && crypto.timingSafeEqual(got, expected);
}`}</Code>
        </Section>
      </div>
    </>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="card space-y-3 p-6 text-sm leading-relaxed text-foreground/85 [&_p_code]:rounded-sm [&_p_code]:bg-muted [&_p_code]:px-1 [&_p_code]:py-0.5 [&_p_code]:font-mono [&_p_code]:text-[0.8em] [&_p_code]:text-foreground">
      <h2 className="text-base font-semibold text-foreground">{title}</h2>
      {children}
    </section>
  );
}

function Code({ children }: { children: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(children);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard unavailable (insecure origin): the text stays selectable.
    }
  };
  return (
    <div className="group relative">
      {/* Dark in both themes: code reads as code, not as another card. */}
      <pre className="overflow-x-auto rounded-md border border-white/5 bg-[#0b0d12] p-4 pr-12 font-mono text-xs leading-relaxed text-[#d6dbe6]">
        {children}
      </pre>
      <button
        onClick={copy}
        aria-label={copied ? "Copié" : "Copier"}
        title={copied ? "Copié" : "Copier"}
        className="absolute top-2 right-2 inline-flex size-7 items-center justify-center rounded-md text-[#8b93a3] transition-colors hover:bg-white/10 hover:text-white"
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </button>
    </div>
  );
}

function Table({ rows }: { rows: [string, string][] }) {
  return (
    <table className="w-full text-sm">
      <tbody className="divide-y">
        {rows.map(([k, v]) => (
          <tr key={k}>
            <td className="py-1.5 pr-4 align-top font-mono text-xs whitespace-nowrap text-primary">{k}</td>
            <td className="py-1.5">{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
