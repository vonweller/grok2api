const MAX_SOURCE_BYTES = 30 << 20;
const MAX_SSO_TOKENS = 10_000;
const MAX_SSO_TOKEN_BYTES = 16 << 10;

export type SSOCredentialParseErrorCode =
  | "accountRowMissingSSO"
  | "empty"
  | "invalidJson"
  | "noTokens"
  | "sourceTooLarge"
  | "tokenTooLarge"
  | "tooMany";

export class SSOCredentialParseError extends Error {
  readonly code: SSOCredentialParseErrorCode;

  constructor(code: SSOCredentialParseErrorCode) {
    super(code);
    this.name = "SSOCredentialParseError";
    this.code = code;
  }
}

export type ParsedSSOCredentials = {
  tokens: string[];
  passwordRowsSanitized: number;
};

type UnknownRecord = Record<string, unknown>;

export async function parseSSOCredentialFiles(files: readonly File[]): Promise<ParsedSSOCredentials> {
  if (files.length === 0) throw new SSOCredentialParseError("empty");
  if (files.reduce((total, file) => total + file.size, 0) > MAX_SOURCE_BYTES) {
    throw new SSOCredentialParseError("sourceTooLarge");
  }

  const result = createCollector();
  for (const file of files) {
    collectText(await file.text(), result);
  }
  return finishCollector(result);
}

export function parseSSOCredentialText(value: string): ParsedSSOCredentials {
  if (new TextEncoder().encode(value).byteLength > MAX_SOURCE_BYTES) {
    throw new SSOCredentialParseError("sourceTooLarge");
  }
  const result = createCollector();
  collectText(value, result);
  return finishCollector(result);
}

type CredentialCollector = {
  tokens: Map<string, string>;
  passwordRowsSanitized: number;
};

function createCollector(): CredentialCollector {
  return { tokens: new Map(), passwordRowsSanitized: 0 };
}

function finishCollector(result: CredentialCollector): ParsedSSOCredentials {
  if (result.tokens.size === 0) throw new SSOCredentialParseError("noTokens");
  return { tokens: [...result.tokens.values()], passwordRowsSanitized: result.passwordRowsSanitized };
}

function collectText(value: string, result: CredentialCollector): void {
  const trimmed = value.trim();
  if (!trimmed) return;

  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    try {
      collectJSONValue(JSON.parse(trimmed) as unknown, result);
      return;
    } catch (error) {
      if (!(error instanceof SyntaxError)) throw error;
      collectLines(trimmed, result, true);
      return;
    }
  }
  collectLines(trimmed, result, false);
}

function collectLines(value: string, result: CredentialCollector, expectJSON: boolean): void {
  for (const rawLine of value.split(/\r?\n/u)) {
    const line = rawLine.trim();
    if (!line) continue;
    if (line.startsWith("{") || line.startsWith("[")) {
      try {
        collectJSONValue(JSON.parse(line) as unknown, result);
      } catch (error) {
        if (error instanceof SyntaxError) throw new SSOCredentialParseError("invalidJson");
        throw error;
      }
      continue;
    }
    if (expectJSON) throw new SSOCredentialParseError("invalidJson");
    collectPlainLine(line, result);
  }
}

function collectJSONValue(value: unknown, result: CredentialCollector): void {
  if (Array.isArray(value)) {
    value.forEach((item) => {
      if (typeof item === "string") collectPlainLine(item.trim(), result);
      else collectJSONObject(item, result, false);
    });
    return;
  }
  collectJSONObject(value, result, false);
}

function collectJSONObject(value: unknown, result: CredentialCollector, allowTokenAlias: boolean): void {
  if (!isRecord(value)) return;

  const directToken = firstStringField(value, allowTokenAlias
    ? ["sso_token", "sso", "sso_cookie", "token"]
    : ["sso_token", "sso", "sso_cookie"]);
  if (directToken) addToken(directToken, result);

  if (Array.isArray(value.cookies)) {
    let fallbackCookie = "";
    for (const cookie of value.cookies) {
      if (!isRecord(cookie) || typeof cookie.name !== "string" || typeof cookie.value !== "string") continue;
      if (!isXAICookie(cookie)) continue;
      const name = cookie.name.trim().toLowerCase();
      if (name === "sso") {
        if (!normalizeSSOToken(cookie.value)) continue;
        addToken(cookie.value, result);
        fallbackCookie = "";
        break;
      }
      if (name === "sso-rw" && !fallbackCookie && normalizeSSOToken(cookie.value)) fallbackCookie = cookie.value;
    }
    if (fallbackCookie) addToken(fallbackCookie, result);
  }

  if (Array.isArray(value.accounts)) {
    value.accounts.forEach((entry) => collectJSONObject(entry, result, true));
  }

  if (isRecord(value.auth)) {
    Object.values(value.auth).forEach((entry) => collectJSONObject(entry, result, false));
  }
}

function firstStringField(value: UnknownRecord, fields: readonly string[]): string {
  for (const field of fields) {
    const candidate = value[field];
    if (typeof candidate === "string" && candidate.trim()) return candidate;
  }
  return "";
}

function collectPlainLine(value: string, result: CredentialCollector): void {
  const legacySeparator = "----";
  const firstLegacySeparator = value.indexOf(legacySeparator);
  if (firstLegacySeparator > 0 && value.slice(0, firstLegacySeparator).includes("@")) {
    const token = value.slice(value.lastIndexOf(legacySeparator) + legacySeparator.length);
    if (!normalizeSSOToken(token)) throw new SSOCredentialParseError("accountRowMissingSSO");
    result.passwordRowsSanitized += 1;
    addToken(token, result);
    return;
  }

  const firstColon = value.indexOf(":");
  const lastColon = value.lastIndexOf(":");
  const possibleEmail = firstColon > 0 ? value.slice(0, firstColon) : value;
  if (firstColon > 0) {
    if (lastColon <= firstColon || !normalizeSSOToken(value.slice(lastColon + 1))) {
      throw new SSOCredentialParseError("accountRowMissingSSO");
    }
    result.passwordRowsSanitized += 1;
    addToken(value.slice(lastColon + 1), result);
    return;
  }
  if (possibleEmail.includes("@")) throw new SSOCredentialParseError("accountRowMissingSSO");
  addToken(value, result);
}

function addToken(value: string, result: CredentialCollector): void {
  const normalized = normalizeSSOToken(value);
  if (!normalized) return;
  if (new TextEncoder().encode(normalized).byteLength > MAX_SSO_TOKEN_BYTES) {
    throw new SSOCredentialParseError("tokenTooLarge");
  }
  if (!result.tokens.has(normalized)) {
    if (result.tokens.size >= MAX_SSO_TOKENS) throw new SSOCredentialParseError("tooMany");
    result.tokens.set(normalized, normalized);
  }
}

function normalizeSSOToken(value: string): string {
  let normalized = value.replace(/[\r\n\0]/gu, "").trim();
  const primaryCookie = normalized.match(/(?:^|;\s*)sso\s*=\s*([^;]+)/iu);
  const fallbackCookie = normalized.match(/(?:^|;\s*)sso-rw\s*=\s*([^;]+)/iu);
  const primaryValue = primaryCookie?.[1]?.trim() ?? "";
  const fallbackValue = fallbackCookie?.[1]?.trim() ?? "";
  if (primaryValue) normalized = primaryValue;
  else if (fallbackValue) normalized = fallbackValue;
  else {
    normalized = normalized.replace(/^(?:sso|sso-rw)\s*=\s*/iu, "");
    normalized = normalized.split(";", 1)[0]?.trim() ?? "";
  }
  return normalized;
}

function isXAICookie(value: UnknownRecord): boolean {
  if (typeof value.domain === "string" && value.domain.trim()) {
    const hostname = value.domain.trim().replace(/^\.+|\.+$/gu, "").toLowerCase();
    return isXAIHostname(hostname);
  }
  if (typeof value.url !== "string" || !value.url.trim()) return false;
  try {
    return isXAIHostname(new URL(value.url).hostname.toLowerCase());
  } catch {
    return false;
  }
}

function isXAIHostname(hostname: string): boolean {
  return hostname === "x.ai" || hostname.endsWith(".x.ai");
}

function isRecord(value: unknown): value is UnknownRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
