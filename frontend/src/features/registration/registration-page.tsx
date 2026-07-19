import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, CheckCircle2, ClipboardPaste, Compass, ExternalLink, FileUp, FolderOpen, KeyRound, MonitorPlay, ShieldCheck, SquareTerminal, TriangleAlert, UserPlus, Webhook, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ApiError } from "@/shared/api/client";
import { CopyButton } from "@/shared/components/copy-button";
import { PageHeader } from "@/shared/components/page-header";
import { formatDateTime } from "@/shared/lib/format";
import {
  importAccounts,
  importConsoleAccounts,
  importWebAccounts,
  pollDeviceAuthorization,
  startDeviceAuthorization,
  type AccountImportResultDTO,
  type AccountProvider,
  type AccountTaskProgressDTO,
  type DeviceSessionDTO,
} from "@/features/accounts/accounts-api";
import { parseSSOCredentialFiles, parseSSOCredentialText, SSOCredentialParseError, type ParsedSSOCredentials } from "@/features/registration/credential-parser";
import {
  getWindowsRegisterStatus,
  importWindowsRegister,
  startWindowsRegister,
  stopWindowsRegister,
  type WindowsRegisterImportResultDTO,
  type WindowsRegisterStatusDTO,
} from "@/features/registration/windows-register-api";

const OFFICIAL_ACCOUNT_URL = "https://accounts.x.ai/";

type ImportDestination = AccountProvider | "web_console";
type DeviceStatus = "idle" | "starting" | "pending" | "succeeded" | "failed" | "expired";
type PreparedSource = ParsedSSOCredentials & { fileCount: number };
type ImportSummary = { provider: AccountProvider; result: AccountImportResultDTO };
type ImportProgress = { provider: AccountProvider; value: AccountTaskProgressDTO };
type PendingImportPlan = Partial<Record<AccountProvider, File[]>>;
type RegistrationOutputSelection = {
  ssoFileCount: number;
  ssoTokenCount: number;
  buildFileCount: number;
};

function isAbortError(error: unknown): boolean {
  return (error instanceof DOMException || error instanceof Error) && error.name === "AbortError";
}

export function RegistrationPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const ssoFileInputRef = useRef<HTMLInputElement>(null);
  const buildFileInputRef = useRef<HTMLInputElement>(null);
  const registrationOutputInputRef = useRef<HTMLInputElement>(null);
  const importAbortRef = useRef<AbortController | null>(null);
  const importCancelledByUserRef = useRef(false);
  const pendingImportPlanRef = useRef<PendingImportPlan | null>(null);
  const [deviceSession, setDeviceSession] = useState<DeviceSessionDTO | null>(null);
  const [deviceStatus, setDeviceStatus] = useState<DeviceStatus>("idle");
  const [destination, setDestination] = useState<ImportDestination>("web_console");
  const [ssoText, setSSOText] = useState("");
  const [preparedSource, setPreparedSource] = useState<PreparedSource | null>(null);
  const [buildFiles, setBuildFiles] = useState<File[]>([]);
  const [sourceParsing, setSourceParsing] = useState(false);
  const [sourceError, setSourceError] = useState("");
  const [importProgress, setImportProgress] = useState<ImportProgress | null>(null);
  const [importSummaries, setImportSummaries] = useState<ImportSummary[]>([]);
  const [importCancelled, setImportCancelled] = useState(false);
  const [registrationOutput, setRegistrationOutput] = useState<RegistrationOutputSelection | null>(null);
  const [registerTarget, setRegisterTarget] = useState(1);
  const [registerEmailMode, setRegisterEmailMode] = useState<"tempmail" | "custom">("tempmail");
  const [registerEmailApi, setRegisterEmailApi] = useState("http://127.0.0.1:8080");
  const [registerEmailDomain, setRegisterEmailDomain] = useState("");
  const [registerProxy, setRegisterProxy] = useState("");
  const [registerMaxMem, setRegisterMaxMem] = useState("");
  const [registerDebug, setRegisterDebug] = useState(false);
  const [registerImportResult, setRegisterImportResult] = useState<WindowsRegisterImportResultDTO | null>(null);
  const logEndRef = useRef<HTMLDivElement>(null);

  const invalidateAccountData = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ["accounts"] });
    void queryClient.invalidateQueries({ queryKey: ["models"] });
    void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
  }, [queryClient]);

  const windowsRegisterQuery = useQuery({
    queryKey: ["windows-register-status"],
    queryFn: getWindowsRegisterStatus,
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === "running" || state === "starting" || state === "stopping" ? 1500 : false;
    },
  });
  const windowsStatus: WindowsRegisterStatusDTO | undefined = windowsRegisterQuery.data;
  const windowsBusy = windowsStatus?.running || windowsStatus?.state === "starting" || windowsStatus?.state === "stopping";

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [windowsStatus?.logs]);

  const startRegisterMutation = useMutation({
    mutationFn: startWindowsRegister,
    onSuccess: (status) => {
      queryClient.setQueryData(["windows-register-status"], status);
      toast.success(t("registration.windows.started"));
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("errors.generic")),
  });
  const stopRegisterMutation = useMutation({
    mutationFn: stopWindowsRegister,
    onSuccess: (status) => {
      queryClient.setQueryData(["windows-register-status"], status);
      toast.success(t("registration.windows.stopped"));
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("errors.generic")),
  });
  const importRegisterMutation = useMutation({
    mutationFn: importWindowsRegister,
    onSuccess: (result) => {
      setRegisterImportResult(result);
      invalidateAccountData();
      const failed = result.results.filter((item) => item.error).length;
      if (failed > 0) toast.warning(t("registration.windows.importPartial", { failed, total: result.results.length }));
      else toast.success(t("registration.windows.importCompleted", { count: result.sourceCount }));
      void windowsRegisterQuery.refetch();
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("errors.generic")),
  });

  const missingLabels = useMemo(() => {
    if (!windowsStatus?.missing?.length) return [];
    return windowsStatus.missing.map((item) => t(`registration.windows.missing.${item}`, { defaultValue: item }));
  }, [t, windowsStatus?.missing]);

  useEffect(() => () => {
    importAbortRef.current?.abort();
    pendingImportPlanRef.current = null;
  }, []);

  useEffect(() => {
    registrationOutputInputRef.current?.setAttribute("webkitdirectory", "");
  }, []);

  useEffect(() => {
    if (!deviceSession || deviceStatus !== "pending") return;
    const controller = new AbortController();
    let timeout = 0;

    const poll = async () => {
      if (Date.now() >= Date.parse(deviceSession.expiresAt)) {
        setDeviceStatus("expired");
        return;
      }
      try {
        const result = await pollDeviceAuthorization(deviceSession.sessionId, controller.signal);
        if (result.status === "succeeded" || result.status === "syncFailed") {
          setDeviceStatus("succeeded");
          invalidateAccountData();
          if (result.status === "syncFailed") toast.warning(t("accounts.createdWithSyncFailure"));
          else toast.success(t("accounts.created"));
          return;
        }
        timeout = window.setTimeout(poll, deviceSession.intervalSeconds * 1000);
      } catch (error) {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && error.status === 429) {
          timeout = window.setTimeout(poll, (deviceSession.intervalSeconds + 5) * 1000);
          return;
        }
        setDeviceStatus("failed");
        toast.error(error instanceof Error ? error.message : t("errors.generic"));
      }
    };

    timeout = window.setTimeout(poll, deviceSession.intervalSeconds * 1000);
    return () => {
      controller.abort();
      window.clearTimeout(timeout);
    };
  }, [deviceSession, deviceStatus, invalidateAccountData, t]);

  const importMutation = useMutation<ImportSummary[], Error, AccountProvider[]>({
    mutationFn: async (targets) => {
      const plan = pendingImportPlanRef.current;
      if (!plan || targets.length === 0) throw new Error(t("errors.generic"));
      const controller = new AbortController();
      importAbortRef.current = controller;
      const summaries: ImportSummary[] = [];

      for (const provider of targets) {
        const files = plan[provider];
        if (!files || files.length === 0) throw new Error(t("errors.generic"));
        const onProgress = (value: AccountTaskProgressDTO) => setImportProgress({ provider, value });
        let result: AccountImportResultDTO;
        if (provider === "grok_web") result = await importWebAccounts(files, onProgress, controller.signal);
        else if (provider === "grok_console") result = await importConsoleAccounts(files, onProgress, controller.signal);
        else result = await importAccounts(files, onProgress, controller.signal);
        summaries.push({ provider, result });
        setImportSummaries([...summaries]);
      }
      return summaries;
    },
    onMutate: () => {
      importCancelledByUserRef.current = false;
      setImportCancelled(false);
      setImportProgress(null);
      setImportSummaries([]);
    },
    onSuccess: (summaries) => {
      setImportCancelled(false);
      const syncFailed = summaries.reduce((total, summary) => total + summary.result.syncFailed, 0);
      setSSOText("");
      setPreparedSource(null);
      setBuildFiles([]);
      setRegistrationOutput(null);
      setSourceError("");
      invalidateAccountData();
      if (syncFailed > 0) toast.warning(t("registration.importCompletedWithFailures", { count: syncFailed }));
      else toast.success(t("registration.importCompleted"));
    },
    onError: (error) => {
      if (isAbortError(error)) {
        if (importCancelledByUserRef.current) {
          setImportCancelled(true);
          toast.warning(t("registration.importCancelledDescription"));
        }
        return;
      }
      toast.error(error.message || t("errors.generic"));
    },
    onSettled: () => {
      importAbortRef.current = null;
      importCancelledByUserRef.current = false;
      pendingImportPlanRef.current = null;
      setImportProgress(null);
      invalidateAccountData();
    },
  });

  async function startDeviceLogin(): Promise<void> {
    setDeviceStatus("starting");
    setDeviceSession(null);
    try {
      const session = await startDeviceAuthorization();
      setDeviceSession(session);
      setDeviceStatus(Date.now() >= Date.parse(session.expiresAt) ? "expired" : "pending");
    } catch (error) {
      setDeviceStatus("failed");
      toast.error(error instanceof Error ? error.message : t("errors.generic"));
    }
  }

  async function selectSSOFiles(files: File[]): Promise<void> {
    if (files.length === 0) return;
    setSourceParsing(true);
    setSourceError("");
    setImportSummaries([]);
    setRegistrationOutput(null);
    try {
      const parsed = await parseSSOCredentialFiles(files);
      setPreparedSource({ ...parsed, fileCount: files.length });
      setSSOText("");
    } catch (error) {
      setPreparedSource(null);
      setSourceError(parseErrorMessage(error));
    } finally {
      setSourceParsing(false);
    }
  }

  async function selectRegistrationOutputDirectory(files: File[]): Promise<void> {
    if (files.length === 0) return;
    const ssoFiles = files.filter(isRegistrationSSOOutputFile);
    const oauthFiles = files.filter(isRegistrationOAuthOutputFile);

    setSourceParsing(true);
    setSourceError("");
    setImportSummaries([]);
    setImportCancelled(false);
    try {
      if (ssoFiles.length === 0 && oauthFiles.length === 0) {
        throw new Error(t("registration.errors.noRegistrationOutputs"));
      }

      const parsed = ssoFiles.length > 0
        ? await parseSSOCredentialFiles(ssoFiles)
        : registrationOutput ? preparedSource : null;
      const nextOAuthFiles = oauthFiles.length > 0
        ? oauthFiles
        : registrationOutput ? buildFiles : [];
      setPreparedSource(parsed ? {
        ...parsed,
        fileCount: ssoFiles.length > 0 ? ssoFiles.length : preparedSource?.fileCount ?? 0,
      } : null);
      setSSOText("");
      setBuildFiles(nextOAuthFiles);
      setRegistrationOutput({
        ssoFileCount: ssoFiles.length > 0 ? ssoFiles.length : registrationOutput?.ssoFileCount ?? 0,
        ssoTokenCount: parsed?.tokens.length ?? 0,
        buildFileCount: nextOAuthFiles.length,
      });
      setDestination(parsed ? "web_console" : "grok_build");
    } catch (error) {
      setPreparedSource(null);
      setBuildFiles([]);
      setRegistrationOutput(null);
      setSourceError(parseErrorMessage(error));
    } finally {
      setSourceParsing(false);
    }
  }

  function parseErrorMessage(error: unknown): string {
    if (error instanceof SSOCredentialParseError) return t(`registration.errors.${error.code}`);
    return error instanceof Error ? error.message : t("errors.generic");
  }

  function submitImport(): void {
    setSourceError("");
    if (destination === "grok_build") {
      if (buildFiles.length === 0) {
        setSourceError(t("registration.errors.noBuildFiles"));
        return;
      }
      pendingImportPlanRef.current = { grok_build: buildFiles };
      importMutation.mutate(["grok_build"]);
      return;
    }

    try {
      const parsed = preparedSource ?? parseSSOCredentialText(ssoText);
      const file = createSSOImportFile(parsed.tokens);
      const targets: AccountProvider[] = destination === "web_console"
        ? ["grok_web", "grok_console"]
        : [destination];
      pendingImportPlanRef.current = Object.fromEntries(targets.map((provider) => [provider, [file]])) as PendingImportPlan;
      importMutation.mutate(targets);
    } catch (error) {
      setSourceError(parseErrorMessage(error));
    }
  }

  function submitRegistrationOutput(): void {
    setSourceError("");
    if (!registrationOutput) return;

    const targets: AccountProvider[] = [];
    const plan: PendingImportPlan = {};
    if (preparedSource) {
      const ssoFile = createSSOImportFile(preparedSource.tokens);
      targets.push("grok_web", "grok_console");
      plan.grok_web = [ssoFile];
      plan.grok_console = [ssoFile];
    }
    if (buildFiles.length > 0) {
      targets.push("grok_build");
      plan.grok_build = buildFiles;
    }
    if (targets.length === 0) {
      setSourceError(t("registration.errors.noRegistrationOutputs"));
      return;
    }

    pendingImportPlanRef.current = plan;
    importMutation.mutate(targets);
  }

  function cancelImport(): void {
    importCancelledByUserRef.current = true;
    importAbortRef.current?.abort();
  }

  function providerLabel(provider: AccountProvider): string {
    if (provider === "grok_web") return "Grok Web";
    if (provider === "grok_console") return "Grok Console";
    return "Grok Build";
  }

  const ssoSourceReady = Boolean(preparedSource || ssoText.trim());
  const canImport = destination === "grok_build" ? buildFiles.length > 0 : ssoSourceReady;
  const progressPercent = importProgress && importProgress.value.total > 0
    ? Math.min(100, Math.round((importProgress.value.completed / importProgress.value.total) * 100))
    : 0;

  return (
    <div className="mx-auto max-w-5xl space-y-5">
      <PageHeader
        title={t("registration.title")}
        description={t("registration.description")}
        actions={<Button asChild variant="secondary" size="sm"><Link to="/accounts">{t("registration.viewAccounts")}<ArrowRight /></Link></Button>}
      />

      <section className="flex gap-3 rounded-lg border border-emerald-500/20 bg-emerald-500/[0.06] p-4" aria-labelledby="registration-safety-title">
        <ShieldCheck className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-400" />
        <div className="space-y-1">
          <h2 id="registration-safety-title" className="text-sm font-medium">{t("registration.safetyTitle")}</h2>
          <p className="text-xs leading-5 text-muted-foreground">{t("registration.safetyDescription")}</p>
        </div>
      </section>

      <WorkflowPanel
        step="W"
        icon={<MonitorPlay className="size-4" />}
        title={t("registration.windows.title")}
        description={t("registration.windows.description")}
      >
        {windowsRegisterQuery.isLoading ? (
          <div className="flex min-h-9 items-center gap-2 text-xs text-muted-foreground"><Spinner />{t("common.loading")}</div>
        ) : null}
        {windowsRegisterQuery.isError ? (
          <p className="flex items-center gap-2 text-xs text-destructive" role="alert">
            <TriangleAlert className="size-4" />
            {windowsRegisterQuery.error instanceof Error ? windowsRegisterQuery.error.message : t("errors.generic")}
          </p>
        ) : null}
        {windowsStatus ? (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Badge variant={windowsStatus.platformSupported ? "secondary" : "destructive"}>
                {windowsStatus.platformSupported ? t("registration.windows.platformOk") : t("registration.windows.platformUnsupported")}
              </Badge>
              <Badge variant={windowsStatus.ready ? "secondary" : "outline"}>
                {windowsStatus.ready ? t("registration.windows.ready") : t("registration.windows.notReady")}
              </Badge>
              <Badge variant="outline">{t(`registration.windows.state.${windowsStatus.state}`, { defaultValue: windowsStatus.state })}</Badge>
              {windowsStatus.lastError ? <span className="text-destructive">{windowsStatus.lastError}</span> : null}
            </div>
            {!windowsStatus.ready && missingLabels.length > 0 ? (
              <p className="text-[11px] leading-5 text-muted-foreground">{t("registration.windows.missingList", { items: missingLabels.join(", ") })}</p>
            ) : null}

            {windowsStatus.platformSupported ? (
              <>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="windows-register-target">{t("registration.windows.target")}</Label>
                    <Input
                      id="windows-register-target"
                      type="number"
                      min={1}
                      max={10000}
                      value={registerTarget}
                      disabled={Boolean(windowsBusy) || startRegisterMutation.isPending}
                      onChange={(event) => setRegisterTarget(Math.max(1, Number(event.target.value) || 1))}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>{t("registration.windows.emailMode")}</Label>
                    <Tabs
                      value={registerEmailMode}
                      onValueChange={(value) => setRegisterEmailMode(value as "tempmail" | "custom")}
                    >
                      <TabsList className="grid h-9 w-full grid-cols-2">
                        <TabsTrigger value="tempmail" disabled={Boolean(windowsBusy)}>{t("registration.windows.tempmail")}</TabsTrigger>
                        <TabsTrigger value="custom" disabled={Boolean(windowsBusy)}>{t("registration.windows.custom")}</TabsTrigger>
                      </TabsList>
                    </Tabs>
                  </div>
                  {registerEmailMode === "custom" ? (
                    <>
                      <div className="space-y-1.5">
                        <Label htmlFor="windows-register-email-api">{t("registration.windows.emailApi")}</Label>
                        <Input id="windows-register-email-api" value={registerEmailApi} disabled={Boolean(windowsBusy)} onChange={(event) => setRegisterEmailApi(event.target.value)} />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="windows-register-email-domain">{t("registration.windows.emailDomain")}</Label>
                        <Input id="windows-register-email-domain" value={registerEmailDomain} disabled={Boolean(windowsBusy)} onChange={(event) => setRegisterEmailDomain(event.target.value)} />
                      </div>
                    </>
                  ) : null}
                  <div className="space-y-1.5">
                    <Label htmlFor="windows-register-proxy">{t("registration.windows.proxy")}</Label>
                    <Input id="windows-register-proxy" value={registerProxy} placeholder="http://127.0.0.1:7890" disabled={Boolean(windowsBusy)} onChange={(event) => setRegisterProxy(event.target.value)} />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="windows-register-max-mem">{t("registration.windows.maxMem")}</Label>
                    <Input id="windows-register-max-mem" value={registerMaxMem} placeholder="4G" disabled={Boolean(windowsBusy)} onChange={(event) => setRegisterMaxMem(event.target.value)} />
                  </div>
                </div>
                <div className="flex items-center justify-between gap-3 rounded-md border p-3">
                  <div className="space-y-0.5">
                    <p className="text-xs font-medium">{t("registration.windows.debug")}</p>
                    <p className="text-[11px] text-muted-foreground">{t("registration.windows.debugHelp")}</p>
                  </div>
                  <Switch checked={registerDebug} disabled={Boolean(windowsBusy)} onCheckedChange={setRegisterDebug} />
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    size="sm"
                    disabled={!windowsStatus.ready || Boolean(windowsBusy) || startRegisterMutation.isPending}
                    onClick={() => startRegisterMutation.mutate({
                      target: registerTarget,
                      emailMode: registerEmailMode,
                      emailApi: registerEmailMode === "custom" ? registerEmailApi : undefined,
                      emailDomain: registerEmailMode === "custom" ? registerEmailDomain : undefined,
                      proxy: registerProxy || undefined,
                      maxMem: registerMaxMem || undefined,
                      debug: registerDebug,
                    })}
                  >
                    {startRegisterMutation.isPending ? <Spinner /> : <MonitorPlay />}
                    {t("registration.windows.start")}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={!windowsBusy || stopRegisterMutation.isPending}
                    onClick={() => stopRegisterMutation.mutate()}
                  >
                    {stopRegisterMutation.isPending ? <Spinner /> : <X />}
                    {t("registration.windows.stop")}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={!windowsStatus.canImportCurrent || importRegisterMutation.isPending}
                    onClick={() => importRegisterMutation.mutate({ scope: "current" })}
                  >
                    {importRegisterMutation.isPending ? <Spinner /> : <FileUp />}
                    {t("registration.windows.importCurrent")}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={!windowsStatus.canImportAll || importRegisterMutation.isPending}
                    onClick={() => importRegisterMutation.mutate({ scope: "all" })}
                  >
                    {importRegisterMutation.isPending ? <Spinner /> : <FileUp />}
                    {t("registration.windows.importAll")}
                  </Button>
                </div>
                <div className="grid gap-2 sm:grid-cols-4">
                  <MetricCard label={t("registration.windows.success")} value={windowsStatus.success} />
                  <MetricCard label={t("registration.windows.failed")} value={windowsStatus.failed} />
                  <MetricCard label={t("registration.windows.rateLimited")} value={windowsStatus.rateLimited} />
                  <MetricCard label={t("registration.windows.percent")} value={`${windowsStatus.percent}%`} />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>{t("registration.windows.logs")}</span>
                    <span>{t("registration.windows.elapsed", { seconds: windowsStatus.elapsedSec })}</span>
                  </div>
                  <div className="max-h-48 overflow-auto rounded-md border bg-muted/30 p-3 font-mono text-[11px] leading-5">
                    {(windowsStatus.logs ?? []).length === 0 ? (
                      <p className="text-muted-foreground">{t("registration.windows.logsEmpty")}</p>
                    ) : (
                      windowsStatus.logs.map((line, index) => <div key={`${index}-${line.slice(0, 24)}`}>{line}</div>)
                    )}
                    <div ref={logEndRef} />
                  </div>
                </div>
                {registerImportResult ? (
                  <div className="grid gap-2 sm:grid-cols-2" aria-live="polite">
                    {registerImportResult.results.map((item) => (
                      <div key={item.provider} className="rounded-md border p-3 text-xs">
                        <div className="mb-1.5 flex items-center justify-between gap-2">
                          <span className="font-medium">{item.provider}</span>
                          {item.error ? <TriangleAlert className="size-4 text-amber-500" /> : <CheckCircle2 className="size-4 text-emerald-600" />}
                        </div>
                        <p className="leading-5 text-muted-foreground">
                          {item.error
                            ? item.error
                            : t("registration.importResult", {
                              created: item.created,
                              updated: item.updated,
                              synced: 0,
                              syncFailed: 0,
                            })}
                        </p>
                      </div>
                    ))}
                  </div>
                ) : null}
              </>
            ) : (
              <p className="text-xs leading-5 text-muted-foreground">{t("registration.windows.unsupportedHelp")}</p>
            )}
          </div>
        ) : null}
      </WorkflowPanel>

      <div className="grid items-start gap-3 lg:grid-cols-2">
        <WorkflowPanel
          step="1"
          icon={<UserPlus className="size-4" />}
          title={t("registration.officialTitle")}
          description={t("registration.officialDescription")}
        >
          <Button asChild size="sm">
            <a href={OFFICIAL_ACCOUNT_URL} target="_blank" rel="noopener noreferrer"><ExternalLink />{t("registration.openOfficial")}</a>
          </Button>
          <p className="text-[11px] leading-5 text-muted-foreground">{t("registration.officialHint")}</p>
        </WorkflowPanel>

        <WorkflowPanel
          step="2"
          icon={<KeyRound className="size-4" />}
          title={t("registration.buildTitle")}
          description={t("registration.buildDescription")}
        >
          {deviceStatus === "idle" ? <Button type="button" size="sm" onClick={() => void startDeviceLogin()}><ExternalLink />{t("registration.startDevice")}</Button> : null}
          {deviceStatus === "starting" ? <div className="flex min-h-9 items-center gap-2 text-xs text-muted-foreground" role="status"><Spinner />{t("registration.deviceStarting")}</div> : null}
          {deviceSession && deviceStatus === "pending" ? (
            <div className="space-y-3" aria-live="polite">
              <div className="space-y-1.5">
                <Label>{t("accounts.userCode")}</Label>
                <div className="relative">
                  <code className="flex h-10 items-center rounded-md border bg-muted/40 px-3 pr-11 font-mono text-base font-semibold tabular-nums">{deviceSession.userCode}</code>
                  <CopyButton value={deviceSession.userCode} className="absolute right-1 top-1/2 size-8 -translate-y-1/2" onCopied={() => toast.success(t("common.copied"))} />
                </div>
              </div>
              <Button asChild type="button" size="sm" className="w-full">
                <a href={deviceSession.verificationUriComplete || deviceSession.verificationUri} target="_blank" rel="noopener noreferrer"><ExternalLink />{t("accounts.openVerification")}</a>
              </Button>
              <div className="flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground">
                <span className="flex items-center gap-2"><Spinner className="size-3" />{t("accounts.waiting")}</span>
                <span>{t("accounts.expiresAt", { time: formatDateTime(deviceSession.expiresAt, i18n.language) })}</span>
              </div>
            </div>
          ) : null}
          {deviceStatus === "succeeded" ? (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-md bg-emerald-500/[0.08] p-3 text-xs" role="status">
              <span className="flex items-center gap-2 text-emerald-700 dark:text-emerald-300"><CheckCircle2 className="size-4" />{t("registration.deviceSucceeded")}</span>
              <Button asChild variant="ghost" size="sm"><Link to="/accounts">{t("registration.viewAccounts")}</Link></Button>
            </div>
          ) : null}
          {deviceStatus === "failed" || deviceStatus === "expired" ? (
            <div className="space-y-3" role="status">
              <p className="flex items-center gap-2 text-xs text-destructive"><TriangleAlert className="size-4" />{t(deviceStatus === "expired" ? "registration.deviceExpired" : "registration.deviceFailed")}</p>
              <Button type="button" variant="secondary" size="sm" onClick={() => void startDeviceLogin()}>{t("registration.retryDevice")}</Button>
            </div>
          ) : null}
        </WorkflowPanel>
      </div>

      <WorkflowPanel
        step="3"
        icon={<FileUp className="size-4" />}
        title={t("registration.importTitle")}
        description={t("registration.importDescription")}
      >
        <input
          ref={registrationOutputInputRef}
          type="file"
          multiple
          className="hidden"
          onChange={(event) => {
            const input = event.currentTarget;
            const files = Array.from(input.files ?? []);
            input.value = "";
            void selectRegistrationOutputDirectory(files);
          }}
        />
        <div className="space-y-3 rounded-md border border-blue-500/25 bg-blue-500/[0.05] p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="flex min-w-0 gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-blue-500/10 text-blue-600 dark:text-blue-300"><FolderOpen className="size-4" /></span>
              <div className="space-y-1">
                <p className="text-sm font-medium">{t("registration.outputDirectoryTitle")}</p>
                <p className="max-w-2xl text-[11px] leading-5 text-muted-foreground">{t("registration.outputDirectoryDescription")}</p>
              </div>
            </div>
            <Button type="button" variant="secondary" size="sm" disabled={importMutation.isPending || sourceParsing} onClick={() => registrationOutputInputRef.current?.click()}>
              {sourceParsing ? <Spinner /> : <FolderOpen />}{sourceParsing ? t("registration.parsing") : t("registration.chooseOutputDirectory")}
            </Button>
          </div>
          {registrationOutput ? (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-md bg-background/75 p-3" role="status">
              <div className="space-y-1 text-xs">
                <p className="flex items-center gap-2 font-medium"><CheckCircle2 className="size-3.5 text-emerald-600" />{t("registration.outputDirectoryReady")}</p>
                <p className="text-[10px] leading-4 text-muted-foreground">{t("registration.outputDirectorySummary", registrationOutput)}</p>
                {preparedSource && preparedSource.passwordRowsSanitized > 0 ? <p className="text-[10px] leading-4 text-muted-foreground">{t("registration.passwordRowsSanitized", { count: preparedSource.passwordRowsSanitized })}</p> : null}
              </div>
              <Button type="button" size="sm" disabled={importMutation.isPending || sourceParsing} onClick={submitRegistrationOutput}>
                {importMutation.isPending ? <Spinner /> : <FileUp />}{importMutation.isPending ? t("registration.importingAction") : t("registration.importAllOutputs")}
              </Button>
            </div>
          ) : null}
        </div>

        <Tabs value={destination} onValueChange={(value) => { setDestination(value as ImportDestination); setSourceError(""); setImportSummaries([]); }}>
          <TabsList className="grid h-auto w-full grid-cols-2 gap-1 p-1 lg:grid-cols-4">
            <TabsTrigger value="web_console" disabled={importMutation.isPending} className="gap-1.5"><Compass className="size-3.5" />Web + Console<Badge variant="secondary" className="ml-1 hidden px-1.5 text-[9px] sm:inline-flex">{t("registration.recommended")}</Badge></TabsTrigger>
            <TabsTrigger value="grok_web" disabled={importMutation.isPending} className="gap-1.5"><Compass className="size-3.5" />Grok Web</TabsTrigger>
            <TabsTrigger value="grok_console" disabled={importMutation.isPending} className="gap-1.5"><Webhook className="size-3.5" />Grok Console</TabsTrigger>
            <TabsTrigger value="grok_build" disabled={importMutation.isPending} className="gap-1.5"><SquareTerminal className="size-3.5" />Grok Build</TabsTrigger>
          </TabsList>
        </Tabs>

        {destination === "grok_build" ? (
          <div className="space-y-3">
            <input
              ref={buildFileInputRef}
              type="file"
              multiple
              accept="application/json,.json"
              className="hidden"
              onChange={(event) => {
                setBuildFiles(Array.from(event.currentTarget.files ?? []));
                setRegistrationOutput(null);
                setSourceError("");
                setImportSummaries([]);
                event.currentTarget.value = "";
              }}
            />
            <div className="flex flex-wrap items-center gap-3 rounded-md border border-dashed p-4">
              <Button type="button" variant="secondary" size="sm" disabled={importMutation.isPending} onClick={() => buildFileInputRef.current?.click()}><FileUp />{t("registration.chooseBuildFiles")}</Button>
              <span className="text-xs text-muted-foreground">{buildFiles.length > 0 ? t("registration.filesSelected", { count: buildFiles.length }) : t("registration.buildFileFormats")}</span>
            </div>
            <p className="text-[11px] leading-5 text-muted-foreground">{t("registration.buildFileHint")}</p>
          </div>
        ) : (
          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_260px]">
            <div className="space-y-2">
              <Label htmlFor="registration-sso-input">{t("registration.pasteLabel")}</Label>
              <Textarea
                id="registration-sso-input"
                className="min-h-44 font-mono text-xs"
                autoComplete="off"
                spellCheck={false}
                value={ssoText}
                disabled={importMutation.isPending || sourceParsing}
                placeholder={t("registration.pastePlaceholder")}
                onChange={(event) => {
                  setSSOText(event.target.value);
                  setPreparedSource(null);
                  setRegistrationOutput(null);
                  setSourceError("");
                  setImportSummaries([]);
                }}
              />
            </div>
            <div className="space-y-3">
              <input
                ref={ssoFileInputRef}
                type="file"
                multiple
                accept="application/json,application/x-ndjson,text/plain,.json,.jsonl,.txt"
                className="hidden"
                onChange={(event) => {
                  const input = event.currentTarget;
                  const files = Array.from(input.files ?? []);
                  input.value = "";
                  void selectSSOFiles(files);
                }}
              />
              <button
                type="button"
                className="flex min-h-28 w-full flex-col items-center justify-center gap-2 rounded-md border border-dashed p-4 text-center transition-colors hover:border-foreground/30 hover:bg-muted/30 disabled:pointer-events-none disabled:opacity-50"
                disabled={importMutation.isPending || sourceParsing}
                onClick={() => ssoFileInputRef.current?.click()}
              >
                {sourceParsing ? <Spinner /> : <FileUp className="size-5 text-muted-foreground" />}
                <span className="text-xs font-medium">{sourceParsing ? t("registration.parsing") : t("registration.chooseSSOFiles")}</span>
                <span className="text-[10px] leading-4 text-muted-foreground">JSON · JSONL · TXT</span>
              </button>
              {preparedSource ? (
                <div className="space-y-1 rounded-md bg-muted/45 p-3" role="status">
                  <p className="flex items-center gap-2 text-xs"><CheckCircle2 className="size-3.5 text-emerald-600" />{t("registration.credentialsReady", { count: preparedSource.tokens.length, files: preparedSource.fileCount })}</p>
                  {preparedSource.passwordRowsSanitized > 0 ? <p className="text-[10px] leading-4 text-muted-foreground">{t("registration.passwordRowsSanitized", { count: preparedSource.passwordRowsSanitized })}</p> : null}
                </div>
              ) : null}
            </div>
          </div>
        )}

        <div className="rounded-md bg-muted/35 p-3 text-[11px] leading-5 text-muted-foreground">
          <p className="flex gap-2"><ShieldCheck className="mt-0.5 size-3.5 shrink-0" />{t(destination === "grok_build" ? "registration.buildPrivacyHint" : "registration.ssoPrivacyHint")}</p>
        </div>

        {sourceError ? <p className="flex items-center gap-2 text-xs text-destructive" role="alert"><TriangleAlert className="size-4" />{sourceError}</p> : null}

        {importProgress ? (
          <div className="space-y-2 rounded-md border p-3" role="status" aria-live="polite">
            <div className="flex items-center justify-between gap-3 text-xs">
              <span>{t(importProgress.value.phase === "syncing" ? "registration.syncing" : "registration.importing", { provider: providerLabel(importProgress.provider) })}</span>
              <span className="tabular-nums text-muted-foreground">{importProgress.value.completed} / {importProgress.value.total}</span>
            </div>
            <div className="h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progressPercent}>
              <div className="h-full rounded-full bg-foreground transition-[width]" style={{ width: `${progressPercent}%` }} />
            </div>
          </div>
        ) : null}

        {importSummaries.length > 0 ? (
          <div className="grid gap-2 sm:grid-cols-2" aria-live="polite">
            {importSummaries.map(({ provider, result }) => (
              <div key={provider} className="rounded-md border p-3 text-xs">
                <div className="mb-1.5 flex items-center justify-between gap-2"><span className="font-medium">{providerLabel(provider)}</span>{result.syncFailed > 0 ? <TriangleAlert className="size-4 text-amber-500" /> : <CheckCircle2 className="size-4 text-emerald-600" />}</div>
                <p className="leading-5 text-muted-foreground">{t("registration.importResult", result)}</p>
              </div>
            ))}
          </div>
        ) : null}

        {importCancelled ? (
          <div className="flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-500/[0.07] p-3" role="alert">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
            <div className="space-y-1 text-xs">
              <p className="font-medium">{t("registration.importCancelledTitle")}</p>
              <p className="leading-5 text-muted-foreground">{t("registration.importCancelledDescription")}</p>
              <Button asChild variant="link" size="sm" className="h-auto px-0 py-0"><Link to="/accounts">{t("registration.viewAccounts")}<ArrowRight /></Link></Button>
            </div>
          </div>
        ) : null}

        {importMutation.isPending ? <p className="text-right text-[10px] leading-4 text-muted-foreground">{t("registration.cancelImportWarning")}</p> : null}

        <div className="flex flex-wrap justify-end gap-2">
          {importMutation.isPending ? <Button type="button" variant="secondary" size="sm" onClick={cancelImport}><X />{t("registration.stopImport")}</Button> : null}
          <Button type="button" size="sm" disabled={!canImport || importMutation.isPending || sourceParsing} onClick={submitImport}>
            {importMutation.isPending ? <Spinner /> : destination === "grok_build" ? <FileUp /> : <ClipboardPaste />}
            {importMutation.isPending ? t("registration.importingAction") : t("registration.importAction")}
          </Button>
        </div>
      </WorkflowPanel>
    </div>
  );
}

const REGISTRATION_SSO_OUTPUT_NAMES = new Set(["accounts.txt", "auth-sessions.jsonl", "grok.txt"]);

function registrationOutputPath(file: File): string {
  return (file.webkitRelativePath || file.name).replaceAll("\\", "/").toLowerCase();
}

function isRegistrationSSOOutputFile(file: File): boolean {
  const path = registrationOutputPath(file);
  const name = path.split("/").at(-1) ?? "";
  return REGISTRATION_SSO_OUTPUT_NAMES.has(name);
}

function isRegistrationOAuthOutputFile(file: File): boolean {
  const path = registrationOutputPath(file);
  const parts = path.split("/");
  const name = parts.at(-1) ?? "";
  return name.endsWith(".json") && parts.includes("authenticated");
}

function createSSOImportFile(tokens: readonly string[]): File {
  return new File([`${tokens.join("\n")}\n`], "grok2api-sso-import.txt", { type: "text/plain" });
}

function WorkflowPanel({ step, icon, title, description, children }: { step: string; icon: ReactNode; title: string; description: string; children: ReactNode }) {
  return (
    <section className="space-y-4 rounded-lg bg-card p-5 shadow-sm ring-1 ring-border/70">
      <header className="flex items-start gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">{icon}</span>
        <div className="min-w-0 space-y-1">
          <div className="flex items-center gap-2"><Badge variant="secondary" className="px-1.5 text-[9px]">{step}</Badge><h2 className="text-sm font-medium">{title}</h2></div>
          <p className="text-xs leading-5 text-muted-foreground">{description}</p>
        </div>
      </header>
      <div className="space-y-3">{children}</div>
    </section>
  );
}

function MetricCard({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="rounded-md border p-3">
      <p className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="mt-1 text-lg font-semibold tabular-nums">{value}</p>
    </div>
  );
}
