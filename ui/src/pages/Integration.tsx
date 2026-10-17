import { ArrowRight, Download } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/common";
import { CopyButton } from "../components/CopyButton";
import { relaixSkill } from "../lib/skill";
import { cn } from "@/lib/utils";

/** The page's sections, in order: the table of contents is built from them. */
const SECTIONS = [
  { id: "auth", label: "Authentification" },
  { id: "send", label: "Envoyer un SMS" },
  { id: "track", label: "Suivre un message" },
  { id: "jobs", label: "Historique" },
  { id: "ussd", label: "Codes USSD" },
  { id: "inbox", label: "SMS reçus" },
  { id: "automations", label: "Automatisations" },
  { id: "webhooks", label: "Webhooks et signature" },
  { id: "errors", label: "Codes d'erreur" },
  { id: "mcp", label: "Assistants IA — MCP" },
  { id: "skill", label: "Skill pour assistants" },
];

export function IntegrationPage() {
  // The API is served by the same gatewayd as this page, so its address is
  // this page's origin.
  const base = window.location.origin;
  const active = useActiveSection();
  // A link from another page (/integration#mcp): the router does not scroll to the anchor itself.
  useEffect(() => {
    const id = window.location.hash.slice(1);
    if (id) document.getElementById(id)?.scrollIntoView();
  }, []);

  return (
    <>
      <PageHeader
        title="Intégration"
        subtitle="SMS, codes USSD, SMS reçus et automatisations depuis vos applications — ou depuis un assistant IA."
      />

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_200px] lg:gap-10">
        <div className="flex min-w-0 flex-col gap-7 lg:order-first text-sm leading-relaxed text-foreground/85 [&_p_code]:rounded-sm [&_p_code]:bg-muted [&_p_code]:px-1.5 [&_p_code]:py-px [&_p_code]:font-mono [&_p_code]:text-[0.85em] [&_p_code]:text-foreground">
          <div className="grid gap-3 sm:grid-cols-2">
            <Tile label="URL de base">
              <span className="flex-1 truncate font-mono text-sm font-medium text-foreground">{base}</span>
              <CopyButton text={base} label="Copier l'URL" />
            </Tile>
            <Tile label="Votre clé API">
              <span className="flex-1 truncate font-mono text-sm font-medium text-foreground">Authorization: Bearer …</span>
            </Tile>
          </div>

          <Section id="auth" title="Authentification">
            <p>
              Chaque requête porte la clé API en en-tête <code>Authorization</code> : celle du serveur (<code>RELAIX_API_KEY</code>), ou une
              clé de votre compte. Les erreurs ont toujours la forme <code>{`{"error": {"code", "message"}}`}</code> : le <code>code</code>{" "}
              est stable, le message peut changer.
            </p>
            <Code>{`Authorization: Bearer $RELAIX_API_KEY`}</Code>
          </Section>

          <Section id="send" title="Envoyer un SMS" method="POST" path="/send">
            <p>
              Répond <code>202 Accepted</code> : la passerelle a pris le message en charge, pas encore envoyé. <code>mode</code> vaut{" "}
              <code>queued</code> (par défaut, attend un appareil) ou <code>immediate</code> (<code>503</code> si aucun appareil n'est prêt).
            </p>
            <Snippets
              snippets={{
                curl: `curl -X POST ${base}/send \\
  -H "Authorization: Bearer $RELAIX_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "recipient": "+22670000000",
    "body": "Votre code est 482913",
    "mode": "immediate",
    "callbackUrl": "https://example.com/hooks/sms"
  }'`,
                JavaScript: `const res = await fetch("${base}/send", {
  method: "POST",
  headers: {
    Authorization: \`Bearer \${process.env.RELAIX_API_KEY}\`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({
    recipient: "+22670000000",
    body: "Votre code est 482913",
  }),
});
// res.status === 202`,
                Python: `import os, requests

r = requests.post(
    "${base}/send",
    headers={"Authorization": f"Bearer {os.environ['RELAIX_API_KEY']}"},
    json={
        "recipient": "+22670000000",
        "body": "Votre code est 482913",
    },
)
assert r.status_code == 202`,
                Go: `body := strings.NewReader(\`{"recipient": "+22670000000", "body": "Votre code est 482913"}\`)
req, _ := http.NewRequest("POST", "${base}/send", body)
req.Header.Set("Authorization", "Bearer "+os.Getenv("RELAIX_API_KEY"))
req.Header.Set("Content-Type", "application/json")

res, err := http.DefaultClient.Do(req)
if err != nil {
	log.Fatal(err)
}
defer res.Body.Close()
// res.StatusCode == 202`,
                Rust: `// reqwest = { version = "0.12", features = ["blocking", "json"] }
let res = reqwest::blocking::Client::new()
    .post("${base}/send")
    .bearer_auth(std::env::var("RELAIX_API_KEY")?)
    .json(&serde_json::json!({
        "recipient": "+22670000000",
        "body": "Votre code est 482913",
    }))
    .send()?;
assert_eq!(res.status(), 202);`,
                Java: `var request = HttpRequest.newBuilder(URI.create("${base}/send"))
    .header("Authorization", "Bearer " + System.getenv("RELAIX_API_KEY"))
    .header("Content-Type", "application/json")
    .POST(HttpRequest.BodyPublishers.ofString("""
        {
          "recipient": "+22670000000",
          "body": "Votre code est 482913"
        }"""))
    .build();

var response = HttpClient.newHttpClient().send(request, HttpResponse.BodyHandlers.ofString());
// response.statusCode() == 202`,
              }}
            />
            <Fields
              rows={[
                ["recipient", "string", "E.164. Ou recipients: [...] (jusqu'à 100) — un job par numéro, réponse {jobs: [...]}.", true],
                ["body", "string", "Texte du message, 1530 caractères au plus. GSM-7 : 160 par SMS ; UCS-2 : 70.", true],
                ["deviceId", "uuid", "Impose un téléphone. Jamais remplacé par un autre."],
                ["subscriptionId", "int", "Avec deviceId : la SIM d'un téléphone multi-SIM (voir GET /devices)."],
                ["mode", '"queued" | "immediate"', "queued (défaut) attend un appareil ; immediate échoue en 503 si aucun n'est prêt."],
                ["priority", "int", "Plus haut = traité d'abord."],
                ["scheduledAt", "RFC 3339", "Envoi différé."],
                ["expiresAt", "RFC 3339", "Au-delà, le message n'est plus envoyé (OTP périmé)."],
                ["callbackUrl", "URL", "Webhook appelé quand le job atteint un état final."],
              ]}
            />
          </Section>

          <Section id="track" title="Suivre un message" method="GET" path="/jobs/{id}">
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

          <Section id="jobs" title="Historique" method="GET" path="/jobs">
            <p>
              Du plus récent au plus ancien, paginé par curseur : passez le <code>nextCursor</code> de la réponse en <code>cursor</code> tant
              qu'il est présent. Filtres combinables : <code>status</code> (liste séparée par des virgules), <code>deviceId</code>,{" "}
              <code>recipient</code> (préfixe), <code>from</code> / <code>to</code> (RFC 3339), <code>limit</code> (200 max).
            </p>
            <Code>{`curl "${base}/jobs?status=failed&recipient=%2B226&limit=50" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
          </Section>

          <Section id="ussd" title="Codes USSD" method="POST" path="/ussd">
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
            <Fields
              rows={[
                ["deviceId", "uuid", "Le téléphone qui compose (GET /devices). Il doit annoncer la capacité ussd.", true],
                ["code", "string", "Composé tel quel : *144#, *144*2*1*70000000*5000#…", true],
                ["replies", "string[]", "Saisis dans l'ordre dans les menus ouverts par le code (capacité ussd_menu)."],
                ["subscriptionId", "int", "La SIM qui compose ; par défaut celle du téléphone."],
                ["sensitive", "bool", "Le code ou une réponse porte un secret : jamais affiché, effacé une fois le job fini."],
                ["mode, priority…", "", "scheduledAt, expiresAt, callbackUrl : comme pour /send."],
              ]}
            />
            <p>
              Le code PIN ne transite jamais : écrivez <code>{"{pin}"}</code> dans le code ou une réponse, le téléphone le remplace depuis
              son coffre — seulement pour un modèle de code approuvé sur le téléphone, dans les plafonds fixés là-bas. Sinon le job échoue
              avec <code>PIN_REFUSED</code>.
            </p>
          </Section>

          <Section id="inbox" title="SMS reçus" method="GET" path="/inbox">
            <p>
              Les SMS que les téléphones transmettent (règles « Reçus » de chaque appareil, ou une automatisation), du plus récent au plus
              ancien. Filtres : <code>deviceId</code>, <code>sender</code> (exact, ex. <code>OrangeMoney</code>), <code>from</code> /{" "}
              <code>to</code>, <code>limit</code>, <code>cursor</code>. Ils partent aussi vers le webhook des SMS reçus, signé comme les
              autres.
            </p>
            <Code>{`curl "${base}/inbox?sender=OrangeMoney&limit=20" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
            <Code>{`{
  "event": "sms.received",
  "data": {
    "messageId": "…",
    "deviceId": "…",
    "subscriptionId": 2,
    "sender": "OrangeMoney",
    "body": "Vous avez recu 5 000 FCFA de 70123456…",
    "receivedAt": "2026-10-16T09:12:00Z",
    "attempt": 1,
    "automationId": "…",
    "fields": {
      "amount": "5 000",
      "from": "70123456"
    },
    "numbers": {
      "amount": 5000
    }
  }
}`}</Code>
            <p>
              <code>automationId</code>, <code>fields</code> et <code>numbers</code> n'apparaissent que si une automatisation a transmis le
              SMS : <code>numbers</code> reprend les champs qui se lisent comme des nombres (« 5 000 » → <code>5000</code>).
            </p>
          </Section>

          <Section id="automations" title="Automatisations" method="GET" path="/automations/runs">
            <p>
              Elles tournent sur le téléphone, même hors ligne : un SMS arrive, un motif en extrait des champs, des actions suivent —
              transmettre, répondre par SMS, appeler un webhook, lancer un code USSD. Elles se règlent dans la page Automatisations, ou via{" "}
              <code>PUT /devices/{"{id}"}/policy</code> (champ <code>automations</code>). <code>GET /automations/library</code> liste les
              modèles prêts à installer — chacun avec sa description et les valeurs qu'il demande ({"{{url}}"}, {"{{sender}}"}…). Ce
              qu'elles ont fait :
            </p>
            <Code>{`curl "${base}/automations/runs?deviceId=$DEVICE_ID&limit=20" \\
  -H "Authorization: Bearer $RELAIX_API_KEY"`}</Code>
            <p>
              Le webhook d'une automatisation est envoyé par le téléphone lui-même et signé avec <strong>son propre secret</strong>, généré
              à la création (bouton copier dans l'éditeur) — pas avec celui de la passerelle. Un corps JSON reste du JSON :{" "}
              <code>{`{"montant": {amount}, "de": "{from}"}`}</code> donne <code>{`{"montant": 5000, "de": "70123456"}`}</code>.
            </p>
          </Section>

          <Section id="webhooks" title="Webhooks et signature">
            <p>
              Trois sources, même format : le <code>callbackUrl</code> d'un job (à chaque état final, avec <code>responses</code> pour un
              USSD), le webhook des SMS reçus, et les webhooks des automatisations. Chaque envoi est réessayé avec un délai croissant tant
              que votre serveur ne répond pas <code>2xx</code>.
            </p>
            <Code>{`X-Relaix-Timestamp: 1767225600
X-Relaix-Signature: sha256=<hex>
X-Relaix-Delivery: <jobId | messageId | id de livraison>

{
  "event": "sms.sent",
  "data": {
    "jobId": "…",
    "kind": "sms",
    "status": "sent",
    "recipient": "+22670000000",
    "partsSent": 1,
    "attempt": 1
  }
}`}</Code>
            <p>
              Toujours la même enveloppe <code>{`{"event": "<nom>", "data": {…}}`}</code> : aiguillez sur <code>event</code>, lisez{" "}
              <code>data</code>.
            </p>
            <Fields
              head={["Événement", "Quand"]}
              rows={[
                ["sms.sent / sms.delivered", "", "SMS parti, puis accusé de réception de l'opérateur (si renvoyé)."],
                ["sms.failed / sms.cancelled", "", "SMS en échec (errorCode, errorMessage) ou annulé."],
                ["ussd.completed", "", "Le réseau a répondu au code : data.responses, un écran par entrée."],
                ["ussd.failed / ussd.cancelled", "", "Code en échec (ex. PIN_REFUSED) ou annulé."],
                ["sms.received", "", "SMS reçu transmis par un téléphone (voir plus haut)."],
                ["automation.triggered", "", "Webhook d'une automatisation dont le corps est laissé vide."],
              ]}
            />
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

          <Section id="errors" title="Codes d'erreur">
            <div className="overflow-x-auto rounded-md border bg-card">
              <table className="w-full min-w-[520px] text-[13px]">
                <thead>
                  <tr className="border-b text-left eyebrow">
                    <th className="w-20 px-4 py-2.5">HTTP</th>
                    <th className="w-56 px-4 py-2.5">code</th>
                    <th className="px-4 py-2.5">Quand</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/60">
                  {ERRORS.map(([http, code, when]) => (
                    <tr key={code}>
                      <td className="px-4 py-2.5">
                        <span
                          className={cn(
                            "rounded-sm px-1.5 py-0.5 font-mono text-xs font-semibold",
                            http >= 500 ? "bg-danger-soft text-destructive" : "bg-warning-soft text-warning",
                          )}
                        >
                          {http}
                        </span>
                      </td>
                      <td className="px-4 py-2.5 font-mono font-medium">{code}</td>
                      <td className="px-4 py-2.5 text-muted-foreground">{when}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Section>

          <Section id="mcp" title="Assistants IA — MCP">
            <p>
              La passerelle est un serveur <a className="underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground" href="https://modelcontextprotocol.io">MCP</a> à{" "}
              <code>{base}/mcp</code> : un assistant (Claude, ou tout client MCP) y trouve des outils pour lister les téléphones, envoyer des
              SMS, lancer des codes USSD, lire les SMS reçus, régler les règles des téléphones (horaires, plafonds, pays, SMS transmis) et écrire
              les automatisations — décrivez-lui en français ce que vous voulez. Il
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
          <Section id="skill" title="Skill pour assistants de code">
            <p>
              Pour qu'un assistant de code (Claude Code, ou tout agent qui lit les <em>skills</em>) intègre la passerelle dans votre
              application sans relire cette page : ce fichier lui apprend l'API, les webhooks et leur signature, l'USSD et les
              automatisations, avec l'adresse de cette passerelle. La clé reste une variable d'environnement.
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" className="btn-secondary" onClick={() => download("SKILL.md", relaixSkill(base))}>
                <Download className="size-4" aria-hidden />
                Télécharger SKILL.md
              </button>
              <span className="text-xs text-muted-foreground">
                À placer dans <code className="font-mono">.claude/skills/relaix/SKILL.md</code> du projet, ou dans{" "}
                <code className="font-mono">~/.claude/skills/relaix/</code> pour tous.
              </span>
            </div>
            <details className="group">
              <summary className="cursor-pointer text-[13px] font-medium underline decoration-foreground/30 underline-offset-4">Voir le fichier</summary>
              <div className="mt-2">
                <Code>{relaixSkill(base)}</Code>
              </div>
            </details>
          </Section>
        </div>

        {/* Narrow: a sticky row of tabs above the content. Wide: a sticky column beside it. */}
        <nav
          aria-label="Sur cette page"
          className="sticky top-0 z-10 order-first -mx-4 -mt-2 flex gap-1 self-start overflow-x-auto border-b bg-background px-4 py-2 md:-mx-8 md:px-8 lg:top-6 lg:order-last lg:mx-0 lg:mt-0 lg:flex-col lg:overflow-visible lg:border-0 lg:bg-transparent lg:p-0 lg:pt-1.5"
        >
          <span className="eyebrow hidden pb-1.5 pl-3 lg:block">Sur cette page</span>
          {SECTIONS.map((s) => (
            <a
              key={s.id}
              href={`#${s.id}`}
              className={cn(
                "shrink-0 border-b-2 px-3 py-1 text-[13px] whitespace-nowrap transition-colors lg:border-b-0 lg:border-l-2",
                active === s.id ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground lg:border-border",
              )}
            >
              {s.label}
            </a>
          ))}
          <div className="mt-4 hidden flex-col gap-1.5 rounded-md border bg-card p-3 lg:flex">
            <span className="text-[13px] font-medium">Tester sans code</span>
            <span className="text-xs text-muted-foreground">La page Envoyer appelle la même API.</span>
            <Link to="/send" className="inline-flex items-center gap-1 text-[13px] font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground">
              Ouvrir Envoyer <ArrowRight className="size-3.5" aria-hidden />
            </Link>
          </div>
        </nav>
      </div>
    </>
  );
}

/** The API's error codes callers meet most (api/*.go). */
const ERRORS: [number, string, string][] = [
  [400, "invalid_json", "Le corps n'est pas du JSON, ou porte un champ inconnu."],
  [400, "invalid_recipient", "Un numéro n'est pas au format E.164."],
  [400, "too_many_recipients", "Plus de 100 destinataires."],
  [400, "body_too_long", "Message au-delà de 1530 caractères."],
  [400, "missing_device", "Code USSD sans deviceId."],
  [401, "unauthorized", "Clé absente, révoquée ou mal formée — ou compte suspendu."],
  [404, "not_found", "Job ou appareil inconnu, ou d'un autre compte."],
  [409, "already_final", "Annulation d'un job déjà terminé."],
  [503, "no_device_available", "mode = immediate et aucun appareil prêt."],
];

/** Which section is in view, for the table of contents. */
function useActiveSection(): string {
  const [active, setActive] = useState(SECTIONS[0]!.id);
  useEffect(() => {
    // The last section whose top has passed the upper third of the view, or
    // the last of all once the page is scrolled to its end: a short final
    // section never reaches the upper third.
    const update = (e?: Event) => {
      const scroller = e?.target instanceof HTMLElement ? e.target : null;
      if (scroller && scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 4) {
        setActive(SECTIONS[SECTIONS.length - 1]!.id);
        return;
      }
      const line = window.innerHeight / 3;
      let current = SECTIONS[0]!.id;
      for (const s of SECTIONS) {
        const el = document.getElementById(s.id);
        if (el && el.getBoundingClientRect().top <= line) current = s.id;
      }
      setActive(current);
    };
    update();
    // Scroll events do not bubble; the page scrolls inside the layout, so listen while they descend.
    document.addEventListener("scroll", update, { capture: true, passive: true });
    return () => document.removeEventListener("scroll", update, { capture: true });
  }, []);
  return active;
}

function Section({ id, title, method, path, children }: { id: string; title: string; method?: "GET" | "POST"; path?: string; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`h-${id}`} className="flex scroll-mt-16 flex-col gap-3 lg:scroll-mt-6">
      <h2 id={`h-${id}`} className="flex flex-wrap items-center gap-2.5 text-xl font-semibold tracking-tight text-foreground">
        {title}
        {method && path && (
          <span className="inline-flex items-center gap-2 font-mono text-[13px] font-medium">
            <span
              className={cn(
                "rounded-sm px-1.5 py-0.5 font-semibold",
                method === "POST" ? "bg-success-soft text-success" : "bg-info-soft text-info",
              )}
            >
              {method}
            </span>
            {path}
          </span>
        )}
      </h2>
      {children}
    </section>
  );
}

function Tile({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-card px-4 py-3.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="flex items-center gap-2">{children}</span>
    </div>
  );
}

/** Code stays dark in both themes: it reads as code, not as another card. */
function Code({ children }: { children: string }) {
  return (
    <div className="flex items-start rounded-md border border-[#2f2e2a] bg-[#10100e]">
      <pre className="min-w-0 flex-1 overflow-x-auto p-4 font-mono text-[13px] leading-[1.7] text-[#e9e6df]">{children}</pre>
      <div className="p-2.5">
        <CopyButton text={children} label="Copier" dark />
      </div>
    </div>
  );
}

/** One request in several languages, behind tabs. */
function Snippets({ snippets }: { snippets: Record<string, string> }) {
  const langs = Object.keys(snippets);
  const [lang, setLang] = useState(langs[0]!);
  const code = snippets[lang]!;
  return (
    <div className="overflow-hidden rounded-md border border-[#2f2e2a] bg-[#10100e]">
      <div className="flex items-center justify-between border-b border-[#2f2e2a] py-1.5 pr-2 pl-2.5">
        <div role="tablist" aria-label="Langage" className="flex min-w-0 gap-0.5 overflow-x-auto">
          {langs.map((l) => (
            <button
              key={l}
              type="button"
              role="tab"
              aria-selected={l === lang}
              onClick={() => setLang(l)}
              className={cn(
                "h-7 rounded-md px-2.5 font-mono text-xs font-medium transition-colors",
                l === lang ? "bg-[#262622] text-[#e9e6df]" : "text-[#9c978c] hover:text-[#e9e6df]",
              )}
            >
              {l}
            </button>
          ))}
        </div>
        <CopyButton text={code} label="Copier" dark />
      </div>
      <pre className="overflow-x-auto p-4 font-mono text-[13px] leading-[1.75] text-[#e9e6df]">{code}</pre>
    </div>
  );
}

/** A request's fields: name (a red star when required), type, what it does. */
function Fields({ rows, head = ["Champ", "Description"] }: { rows: [string, string, string, boolean?][]; head?: [string, string] }) {
  const typed = rows.some(([, type]) => type !== "");
  return (
    <div className="overflow-x-auto rounded-md border bg-card">
      <table className="w-full min-w-[520px] text-[13px]">
        <thead>
          <tr className="border-b text-left eyebrow">
            <th className="px-4 py-2.5">{head[0]}</th>
            {typed && <th className="px-4 py-2.5">Type</th>}
            <th className="px-4 py-2.5">{head[1]}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border/60">
          {rows.map(([name, type, desc, required]) => (
            <tr key={name}>
              <td className="px-4 py-2.5 align-top font-mono font-medium whitespace-nowrap text-foreground">
                {name}
                {required && <span className="text-destructive"> *</span>}
              </td>
              {typed && <td className="px-4 py-2.5 align-top font-mono text-xs whitespace-nowrap text-muted-foreground">{type}</td>}
              <td className="px-4 py-2.5 align-top text-muted-foreground">{desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Saves text as a file, from the browser. */
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}
