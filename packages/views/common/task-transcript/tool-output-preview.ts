// Bound work before JSON decoding, redaction, whitespace folding, or DOM mounting.
// Keep the usual daemon's 8 KiB record unchanged, with a separate line budget
// for pathological blank-line output. These limits apply only to previews.
export const TOOL_OUTPUT_MAX_CHARS = 16_384;
export const TOOL_OUTPUT_MAX_LINES = 200;

/** Decode exactly one complete transport-string layer; leave other text alone. */
export function unwrapToolOutput(raw: string): string {
  const trimmed = raw.trim();
  if (trimmed.length < 2 || !trimmed.startsWith('"') || !trimmed.endsWith('"')) return raw;
  try {
    const decoded: unknown = JSON.parse(trimmed);
    return typeof decoded === "string" ? decoded : raw;
  } catch {
    return raw;
  }
}

export function boundToolOutput(raw: string): { text: string; clipped: boolean } {
  let text = raw.slice(0, TOOL_OUTPUT_MAX_CHARS);
  let clipped = raw.length > text.length;
  if (clipped && text.trimStart().startsWith('"')) {
    // A quoted transport string may end mid-escape. Removing at most one
    // unicode escape lets JSON.parse decode only this bounded prefix.
    for (let trim = 0; trim <= 6; trim++) {
      try {
        const decoded: unknown = JSON.parse(`${text.slice(0, text.length - trim)}"`);
        if (typeof decoded === "string") { text = decoded; break; }
      } catch { /* Try the preceding complete escape boundary. */ }
    }
  } else {
    text = unwrapToolOutput(text);
  }

  let end = text.length;
  let line = 1;
  for (let cursor = 0; cursor < text.length; cursor++) {
    const char = text.charCodeAt(cursor);
    if (char !== 10 && char !== 13) continue;
    if (line === TOOL_OUTPUT_MAX_LINES) { end = cursor; break; }
    line++;
    if (char === 13 && text.charCodeAt(cursor + 1) === 10) cursor++;
  }
  clipped ||= end < text.length;
  if (clipped) {
    const last = text.charCodeAt(end - 1);
    if (last === 13 || (last >= 0xd800 && last <= 0xdbff)) end--;
  }
  return { text: text.slice(0, end), clipped };
}
