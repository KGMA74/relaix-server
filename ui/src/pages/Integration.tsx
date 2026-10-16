import { Check, Copy } from "lucide-react";
import { useState, type ReactNode } from "react";
import { PageHeader } from "../components/common";

export function IntegrationPage() {
  // The API is served by the same gatewayd as this page, so its address is
  // this page's origin.
  const base = window.location.origin;

  return (
    <>
      <PageHeader
        title="Intégration"
        subtitle="SMS, codes USSD, SMS reçus et automatisations depuis vos applications — ou depuis un assistant IA."
      />

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

        <Section title="Codes USSD — POST /ussd">
          <p>
            Lance un code sur un téléphone précis (<code>deviceId</code> obligatoire : la réponse concerne le compte de sa SIM). Le job se
            suit comme un SMS ; une fois fini, <code>responses</code> contient chaque écran affiché par le réseau.
          </p>
          <Code>{`curl -X POST ${base}/ussd \\
  -H "Authorization: Bearer $RELAIX_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "deviceId": "$DEVICE_ID",
    "code": "*144#",
    "replies": ["2", "1"]
  }'`}</Code>
          <Table
            rows={[
              ["deviceId", "Le téléphone qui compose (GET /devices). Il doit annoncer la capacité ussd."],
              ["code", "Composé tel quel : *144#, *144*2*1*70000000*5000#…"],
              ["replies", "Optionnel : saisis dans l'ordre dans les menus ouverts par le code (capacité ussd_menu)."],
              ["subscriptionId", "Optionnel : la SIM qui compose ; par défaut celle du téléphone."],
              ["sensitive", "true si le code ou une réponse porte un secret : jamais affiché, effacé une fois le job fini."],
              ["mode, priority, scheduledAt, expiresAt, callbackUrl", "Comme pour /send."],
            ]}
          />
          <p>
            Le code PIN ne transite jamais : écrivez <code>{"{pin}"}</code> dans le code ou une réponse, le téléphone le remplace depuis
            son coffre — seulement pour un modèle de code approuvé sur le téléphone, dans les plafonds fixés là-bas. Sinon le job échoue
            avec <code>PIN_REFUSED</code>.
          </p>
        </Section>

        <Section title="SMS reçus — GET /inbox">
          <p>
            Les SMS que les téléphones transmettent (règles « Reçus » de chaque appareil, ou une automatisation), du plus récent au plus
            ancien. Filtres : <code>deviceId</code>, <code>sender</code> (exact, ex. <code>OrangeMoney</code>), <code>from</code> /{" "}
            <code>to</code>, <code>limit</code>, <code>cursor</code>. Ils partent aussi vers le webhook des SMS reçus, signé comme les
            autres.
          </p>
          <Code>{`curl "${base}/inbox?sender=OrangeMoney&limit=20" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
          <Code>{`{"type":"sms.received","messageId":"…","deviceId":"…","subscriptionId":2,
 "sender":"OrangeMoney","body":"Vous avez recu 5 000 FCFA de 70123456…",
 "receivedAt":"2026-10-16T09:12:00Z","attempt":1,
 "automationId":"…","fields":{"amount":"5 000","from":"70123456"},"numbers":{"amount":5000}}`}</Code>
          <p>
            <code>automationId</code>, <code>fields</code> et <code>numbers</code> n'apparaissent que si une automatisation a transmis le
            SMS : <code>numbers</code> reprend les champs qui se lisent comme des nombres (« 5 000 » → <code>5000</code>).
          </p>
        </Section>

        <Section title="Automatisations">
          <p>
            Elles tournent sur le téléphone, même hors ligne : un SMS arrive, un motif en extrait des champs, des actions suivent —
            transmettre, répondre par SMS, appeler un webhook, lancer un code USSD. Elles se règlent dans la page Automatisations, ou via{" "}
            <code>PUT /devices/{"{id}"}/policy</code> (champ <code>automations</code>). Ce qu'elles ont fait :
          </p>
          <Code>{`curl "${base}/automations/runs?deviceId=$DEVICE_ID&limit=20" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
          <p>
            Le webhook d'une automatisation est envoyé par le téléphone lui-même et signé avec <strong>son propre secret</strong>, généré
            à la création (bouton copier dans l'éditeur) — pas avec celui de la passerelle. Un corps JSON reste du JSON :{" "}
            <code>{`{"montant": {amount}, "de": "{from}"}`}</code> donne <code>{`{"montant": 5000, "de": "70123456"}`}</code>.
          </p>
        </Section>

        <Section title="Webhooks et signature">
          <p>
            Trois sources, même format : le <code>callbackUrl</code> d'un job (à chaque état final, avec <code>responses</code> pour un
            USSD), le webhook des SMS reçus, et les webhooks des automatisations. Chaque envoi est réessayé avec un délai croissant tant
            que votre serveur ne répond pas <code>2xx</code>.
          </p>
          <Code>{`X-Relaix-Timestamp: 1767225600
X-Relaix-Signature: sha256=<hex>
X-Relaix-Delivery: <jobId | messageId | id de livraison>

{"jobId":"…","kind":"sms","status":"sent","recipient":"+237670000000","partsSent":1,"attempt":1}`}</Code>
          <p>
            <code>X-Relaix-Delivery</code> est <strong>le même à chaque nouvel essai</strong> : notez les ids traités et répondez{" "}
            <code>2xx</code> sans rien refaire à un id déjà vu — un paiement n'est jamais compté deux fois. La signature est un
            HMAC-SHA256 de <code>timestamp + "." + corps brut</code> : avec <code>RELAIX_CALLBACK_SECRET</code> (ou le secret du compte)
            pour les jobs et les SMS reçus, avec le secret de l'automatisation pour ses webhooks.
          </p>
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

        <Section title="Assistants IA — MCP">
          <p>
            La passerelle est un serveur <a className="text-primary hover:underline" href="https://modelcontextprotocol.io">MCP</a> à{" "}
            <code>{base}/mcp</code> : un assistant (Claude, ou tout client MCP) y trouve des outils pour lister les téléphones, envoyer des
            SMS, lancer des codes USSD, lire les SMS reçus et écrire les automatisations — décrivez-lui en français ce que vous voulez. Il
            n'a que les droits de la clé utilisée : créez-en une pour lui, la révoquer le déconnecte.
          </p>
          <p>
            <strong>Claude Code</strong> (ou tout client qui envoie un en-tête) :
          </p>
          <Code>{`claude mcp add --transport http relaix ${base}/mcp \\
  --header "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
          <p>
            <strong>claude.ai</strong> : Paramètres → Connecteurs → Ajouter un connecteur personnalisé, adresse <code>{base}/mcp</code>,
            sans identifiant client. Une page de la passerelle demande alors la clé API, une seule fois ; l'assistant reçoit des jetons
            qui se renouvellent seuls pendant 30 jours tant que la clé est valide.
          </p>
          <p>
            Garde-fous : les plafonds et règles des téléphones s'appliquent ; le PIN reste dans le coffre du téléphone, et un code avec{" "}
            <code>{"{pin}"}</code> ne part qu'après approbation sur le téléphone ; le texte des SMS reçus est présenté à l'assistant comme
            une donnée, jamais comme une consigne.
          </p>
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
