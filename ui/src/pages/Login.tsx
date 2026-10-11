import { KeyRound, Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth";
import { ErrorNote } from "../components/common";

export function LoginPage() {
  const { signIn } = useAuth();
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // Checked against a real route before it is kept, so a typo is caught
      // here rather than as a wall of errors on the first page.
      await api.verifyKey(key.trim());
      signIn(key.trim());
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? new Error("Clé API invalide.") : err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative flex min-h-svh items-center justify-center overflow-hidden px-4">
      {/* A soft glow of the brand colour behind the card, so the screen is not just a box on grey. */}
      <div
        aria-hidden
        className="pointer-events-none absolute top-1/2 left-1/2 size-[36rem] -translate-x-1/2 -translate-y-1/2 rounded-full bg-primary/15 blur-3xl"
      />
      <form onSubmit={submit} className="card relative w-full max-w-sm p-8">
        <div className="mb-6 flex items-center gap-3">
          <img src="/ui/favicon.svg" alt="" className="size-10 rounded-lg" />
          <div>
            <h1 className="text-title font-semibold">Relaix</h1>
            <p className="text-sm text-muted-foreground">Passerelle SMS — console</p>
          </div>
        </div>

        <label className="label" htmlFor="key">
          Clé API
        </label>
        <input
          id="key"
          type="password"
          className="input font-mono"
          autoComplete="current-password"
          autoFocus
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="RELAIX_API_KEY"
        />
        <p className="mt-2 text-xs text-muted-foreground">
          La valeur de <code>RELAIX_API_KEY</code> du serveur. Elle reste dans cet onglet et disparaît à sa fermeture.
        </p>

        <div className="mt-4">
          <ErrorNote error={error} />
        </div>

        <button type="submit" className="btn-primary mt-4 w-full" disabled={busy || !key.trim()}>
          {busy ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <KeyRound className="size-4" aria-hidden />}
          {busy ? "Vérification…" : "Se connecter"}
        </button>
      </form>
    </div>
  );
}
