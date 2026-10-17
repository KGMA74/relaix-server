/**
 * A request as a curl command, to copy into a terminal or a script. The key is
 * $RELAIX_API_KEY, never the one this tab holds: the command gets pasted into
 * tickets and chats.
 */
export function curlFor(path: string, body: unknown): string {
  // Single quotes around the body: one inside it closes, escapes and reopens.
  const json = JSON.stringify(body, null, 2).replace(/'/g, "'\\''");
  return [
    `curl -X POST ${window.location.origin}${path} \\`,
    `  -H "Authorization: Bearer $RELAIX_API_KEY" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '${json.replace(/\n/g, "\n  ")}'`,
  ].join("\n");
}
