import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";

/** Copies text; `dark` for one laid on a code block, which also says "Copier". */
export function CopyButton({ text, label, dark }: { text: string; label: string; dark?: boolean }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard unavailable (insecure origin): the text stays selectable.
    }
  };
  return (
    <button
      type="button"
      onClick={copy}
      aria-label={copied ? "Copié" : label}
      title={copied ? "Copié" : label}
      className={cn(
        "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border px-2 text-xs font-medium transition-colors",
        dark
          ? copied
            ? "border-[#62c08d]/35 bg-[#62c08d]/10 text-[#62c08d]"
            : "border-[#3d3c37] bg-[#22221f] text-[#9c978c] hover:text-white"
          : "text-muted-foreground hover:bg-accent hover:text-foreground",
      )}
    >
      {copied ? <Check className="size-3.5" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
      {dark && (copied ? "Copié" : "Copier")}
    </button>
  );
}
