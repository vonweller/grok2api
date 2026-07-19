import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, createValidatedDecoder, hasShape, isArrayOf, isBoolean, isNumber, isOptional, isString } from "@/shared/api/decoder";

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
  destinations?: Array<"grok_web" | "grok_console">;
};

export type WindowsRegisterProviderImportDTO = {
  provider: string;
  created: number;
  updated: number;
  skipped: number;
  error?: string;
};

export type WindowsRegisterImportResultDTO = {
  scope: string;
  sourceCount: number;
  results: WindowsRegisterProviderImportDTO[];
};

const decodeStatus = createObjectDecoder<WindowsRegisterStatusDTO>("windows register status", {
  platformSupported: isBoolean,
  ready: isBoolean,
  missing: isArrayOf(isString),
  browserInstalled: isBoolean,
  state: isString as (value: unknown) => value is WindowsRegisterState,
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
  startedAt: isOptional(isString),
  finishedAt: isOptional(isString),
  elapsedSec: isNumber,
  exitCode: isOptional(isNumber),
  lastError: isString,
  logs: isArrayOf(isString),
});

const decodeImportResult = createValidatedDecoder<WindowsRegisterImportResultDTO>("windows register import result", hasShape({
  scope: isString,
  sourceCount: isNumber,
  results: isArrayOf(hasShape({
    provider: isString,
    created: isNumber,
    updated: isNumber,
    skipped: isNumber,
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
