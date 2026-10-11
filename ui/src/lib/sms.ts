// SMS segment counting, so the send form can say what a message will cost
// before it goes out. The handset does the real split; this mirrors the GSM
// 03.38 rules it follows.

const GSM7_BASIC =
  "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
  "¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà";

// Extension table characters cost two septets: an escape, then the character.
const GSM7_EXTENDED = "^{}\\[~]|€\f";

export interface Segments {
  encoding: "GSM-7" | "UCS-2";
  /** Length in the encoding's units: septets for GSM-7, UTF-16 units for UCS-2. */
  units: number;
  parts: number;
  /** Units left before the next part starts. */
  remaining: number;
}

export function countSegments(text: string): Segments {
  let septets = 0;
  let gsm = true;
  for (const ch of text) {
    if (GSM7_BASIC.includes(ch)) septets += 1;
    else if (GSM7_EXTENDED.includes(ch)) septets += 2;
    else {
      gsm = false;
      break;
    }
  }

  // A single part holds 160 septets or 70 UCS-2 units; once concatenated, each
  // part gives up room for the reassembly header.
  const [units, single, multi] = gsm ? [septets, 160, 153] : [text.length, 70, 67];
  if (units === 0) return { encoding: gsm ? "GSM-7" : "UCS-2", units, parts: 0, remaining: single };
  const parts = units <= single ? 1 : Math.ceil(units / multi);
  const capacity = parts === 1 ? single : parts * multi;
  return { encoding: gsm ? "GSM-7" : "UCS-2", units, parts, remaining: capacity - units };
}

/** Same shape check as the server: a plus sign then 8 to 15 digits, first non-zero. */
export function isE164(s: string): boolean {
  return /^\+[1-9]\d{7,14}$/.test(s);
}

/** Splits a free-form list of numbers on commas, semicolons, spaces and newlines. */
export function parseRecipients(raw: string): string[] {
  return [
    ...new Set(
      raw
        .split(/[\s,;]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  ];
}
