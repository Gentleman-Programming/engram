const PRIVATE_TAG_PATTERN = /<private>[\s\S]*?<\/private>/gi;

/**
 * Redact explicit private blocks before content leaves the Pi session.
 * This is a convenience convention, not a general-purpose secret scanner.
 */
export function redactPrivateTags(value) {
  return value.replace(PRIVATE_TAG_PATTERN, "[REDACTED]");
}

/**
 * Redact private blocks in a relative URL path, including decoded query values.
 */
export function redactUrlPath(path) {
  const redactedPath = redactPrivateTags(path);
  try {
    const url = new URL(redactedPath, "http://engram.local");
    const redactedParams = new URLSearchParams();
    for (const [key, value] of url.searchParams.entries()) {
      redactedParams.append(key, redactPrivateTags(value));
    }
    const query = redactedParams.toString();
    return `${url.pathname}${query ? `?${query}` : ""}${url.hash}`;
  } catch {
    return redactedPath;
  }
}

/**
 * Recursively redact strings in payloads sent to Engram.
 * Preserves object/array shape and leaves non-string primitives unchanged.
 */
export function redactValue(value) {
  if (typeof value === "string") return redactPrivateTags(value);
  if (Array.isArray(value)) return value.map((entry) => redactValue(entry));
  if (value && typeof value === "object") {
    const redacted = {};
    for (const [key, entry] of Object.entries(value)) {
      redacted[key] = redactValue(entry);
    }
    return redacted;
  }
  return value;
}
