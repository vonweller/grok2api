import { apiRequest } from "@/shared/api/client";
import { createValidatedDecoder, hasShape, isArrayOf, isBoolean, isNumber, isObject, isOptional, isString, type ValueValidator } from "@/shared/api/decoder";

export type WindowsRegisterState = "idle" | "starting" | "running" | "stopping" | "stopped" | "completed" | "error";

export type WindowsRegisterStatusDTO = {
  platformSupported: boolean;
  ready: boolean;
  missing: string[];
  browserInstalled: boolean;
  state: WindowsRegisterState;
  running: boolean;
  target: number;
  success: number;
  failed: number;
  rateLimited: number;
  percent: number;
  generatedThisRun: number;
  generatedTotal: number;
  canImportCurrent: boolean;
  canImportAll: boolean;
  startedAt?: string;
  finishedAt?: string;
  elapsedSec: number;
  exitCode?: number;
  lastError: string;
  logs: string[];
};

// Go may emit null for unset pointer fields; treat null like omitted.
const isNullableString: ValueValidator = (value) => value === null || isOptional(isString)(value);
const isNullableNumber: ValueValidator = (value) => value === null || isOptional(isNumber)(value);
const isStringArray: ValueValidator = (value) => value === null || isArrayOf(isString)(value);

export type WindowsRegisterStartInput = {
  target: number;
  emailMode: "tempmail" | "custom";
  emailApi?: string;
  emailDomain?: string;
  proxy?: string;
  maxMem?: string;
  debug?: boolean;
};

export type WindowsRegisterImportInput = {
  scope: "current" | "all";
  destinations?: Array<"grok_web" | "grok_console" | "grok_build">;
};

export type WindowsRegisterProviderImportDTO = {
  provider: string;
  created: number;
  updated: number;
  linked?: number;
  skipped: number;
  failed?: number;
  error?: string;
};

export type WindowsRegisterImportResultDTO = {
  scope: string;
  sourceCount: number;
  results: WindowsRegisterProviderImportDTO[];
};

const decodeStatus = createValidatedDecoder<WindowsRegisterStatusDTO>("windows register status", (value) => {
  if (!isObject(value)) return false;
  const record = value as Record<string, unknown>;
  const shapeOk = hasShape({
    platformSupported: isBoolean,
    ready: isBoolean,
    missing: isStringArray,
    browserInstalled: isBoolean,
    state: isString,
    running: isBoolean,
    target: isNumber,
    success: isNumber,
    failed: isNumber,
    rateLimited: isNumber,
    percent: isNumber,
    generatedThisRun: isNumber,
    generatedTotal: isNumber,
    canImportCurrent: isBoolean,
    canImportAll: isBoolean,
    startedAt: isNullableString,
    finishedAt: isNullableString,
    elapsedSec: isNumber,
    exitCode: isNullableNumber,
    lastError: isString,
    logs: isStringArray,
  })(record);
  if (!shapeOk) return false;
  // Normalize nulls so UI state stays simple.
  if (record.missing == null) record.missing = [];
  if (record.logs == null) record.logs = [];
  if (record.startedAt == null) delete record.startedAt;
  if (record.finishedAt == null) delete record.finishedAt;
  if (record.exitCode == null) delete record.exitCode;
  if (record.lastError == null) record.lastError = "";
  return true;
});

const decodeImportResult = createValidatedDecoder<WindowsRegisterImportResultDTO>("windows register import result", hasShape({
  scope: isString,
  sourceCount: isNumber,
  results: isArrayOf(hasShape({
    provider: isString,
    created: isNumber,
    updated: isNumber,
    linked: isOptional(isNumber),
    skipped: isNumber,
    failed: isOptional(isNumber),
    error: isOptional(isString),
  })),
}));

export function getWindowsRegisterStatus(): Promise<WindowsRegisterStatusDTO> {
  return apiRequest("/api/admin/v1/accounts/windows-register/status", { method: "GET" }, decodeStatus);
}

export function startWindowsRegister(input: WindowsRegisterStartInput): Promise<WindowsRegisterStatusDTO> {
  return apiRequest("/api/admin/v1/accounts/windows-register/start", { method: "POST", body: input }, decodeStatus);
}

export function stopWindowsRegister(): Promise<WindowsRegisterStatusDTO> {
  return apiRequest("/api/admin/v1/accounts/windows-register/stop", { method: "POST", body: {} }, decodeStatus);
}

export function importWindowsRegister(input: WindowsRegisterImportInput): Promise<WindowsRegisterImportResultDTO> {
  return apiRequest("/api/admin/v1/accounts/windows-register/import", { method: "POST", body: input }, decodeImportResult);
}
