"use client";

import dynamic from "next/dynamic";
import { useMemo, useRef, useState } from "react";
import { json } from "@codemirror/lang-json";
import { Check, CircleAlert, Clock3, Copy, FileUp, Play, RotateCcw, Square } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button, IconButton } from "@/components/ui/button";
import { Input, Select, Switch } from "@/components/ui/form";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { ContentFrame } from "./shared";

const CodeMirror = dynamic(() => import("@uiw/react-codemirror"), { ssr: false, loading: () => <div className="h-full animate-pulse bg-subtle" /> });

const examples: Record<string, string> = {
  "/v1/models": "",
  "/v1/chat/completions": JSON.stringify({ model: "grok-4.5", messages: [{ role: "user", content: "Explain this API in one sentence." }], stream: true }, null, 2),
  "/v1/responses": JSON.stringify({ model: "grok-4.5", input: "Write a concise release note.", stream: true }, null, 2),
  "/v1/messages": JSON.stringify({ model: "grok-4.5", max_tokens: 256, messages: [{ role: "user", content: "Hello" }], stream: true }, null, 2),
  "/v1/images/generations": JSON.stringify({ model: "grok-imagine-image", prompt: "A clean product photo on a neutral background", n: 1 }, null, 2),
  "/v1/images/edits": JSON.stringify({ model: "grok-imagine-image-edit", prompt: "Use a white studio background" }, null, 2),
  "/v1/videos": JSON.stringify({ model: "grok-imagine-video", prompt: "A slow camera orbit around the product" }, null, 2),
};

type RequestMode = "json" | "multipart";
interface DebugResponse { status: number; duration_ms: number; headers: Record<string, string>; body: unknown; streaming?: boolean }

const defaultEndpoint = "/v1/chat/completions";

function canStream(endpoint: string) {
  return endpoint.includes("chat") || endpoint.includes("responses") || endpoint.includes("messages");
}

export function DebuggerView() {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [endpoint, setEndpoint] = useState(defaultEndpoint);
  const [method, setMethod] = useState("POST");
  const [mode, setMode] = useState<RequestMode>("json");
  const [stream, setStream] = useState(true);
  const [apiKey, setApiKey] = useState("");
  const [body, setBody] = useState(examples[defaultEndpoint]);
  const [file, setFile] = useState<File | null>(null);
  const [response, setResponse] = useState<DebugResponse | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  const controller = useRef<AbortController | null>(null);
  const jsonExtension = useMemo(() => [json()], []);
  const streamSupported = method !== "GET" && canStream(endpoint);

  function changeEndpoint(next: string) {
    const nextMethod = next === "/v1/models" ? "GET" : "POST";
    setEndpoint(next);
    setBody(examples[next]);
    setResponse(null);
    setError("");
    setMethod(nextMethod);
    setStream(nextMethod !== "GET" && canStream(next));
    if (nextMethod === "GET") setMode("json");
  }

  function changeMethod(next: string) {
    setMethod(next);
    if (next === "GET") {
      setStream(false);
      setMode("json");
    } else if (canStream(endpoint)) {
      setStream(true);
    }
  }

  function reset() {
    setBody(examples[endpoint]);
    setResponse(null);
    setError("");
    setFile(null);
    setElapsed(0);
  }

  function parsePayload() {
    const parsed = body.trim() ? JSON.parse(body) as Record<string, unknown> : {};
    if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("object");
    return streamSupported ? { ...parsed, stream } : parsed;
  }

  async function run() {
    setRunning(true);
    setError("");
    setResponse(null);
    setElapsed(0);
    let parsed: Record<string, unknown>;
    try {
      parsed = parsePayload();
    } catch {
      setError(locale === "zh" ? "请求正文必须是有效的 JSON 对象。" : "Request body must be a valid JSON object.");
      setRunning(false);
      return;
    }

    const abort = new AbortController();
    controller.current = abort;
    const started = performance.now();
    const timer = window.setInterval(() => setElapsed(Math.round(performance.now() - started)), 100);
    const headers = new Headers({ Accept: streamSupported && stream ? "text/event-stream, application/json" : "application/json" });
    if (apiKey.trim()) headers.set("Authorization", `Bearer ${apiKey.trim()}`);
    let requestBody: BodyInit | undefined;

    if (method !== "GET") {
      if (mode === "multipart") {
        const form = new FormData();
        for (const [key, value] of Object.entries(parsed)) {
          if (value !== undefined && value !== null) form.append(key, typeof value === "object" ? JSON.stringify(value) : String(value));
        }
        if (file) form.append("image", file, file.name);
        requestBody = form;
      } else {
        headers.set("Content-Type", "application/json");
        requestBody = JSON.stringify(parsed);
      }
    }

    try {
      const result = await fetch(endpoint, { method, headers, body: requestBody, signal: abort.signal });
      const responseHeaders = Object.fromEntries(result.headers.entries());
      const base = { status: result.status, duration_ms: 0, headers: responseHeaders };
      if (streamSupported && stream && result.body) {
        const reader = result.body.getReader();
        const decoder = new TextDecoder();
        let transcript = "";
        setResponse({ ...base, body: transcript, streaming: true });
        for (;;) {
          const chunk = await reader.read();
          if (chunk.done) break;
          transcript += decoder.decode(chunk.value, { stream: true });
          setResponse({ ...base, duration_ms: Math.round(performance.now() - started), body: transcript, streaming: true });
        }
        transcript += decoder.decode();
        setResponse({ ...base, duration_ms: Math.round(performance.now() - started), body: transcript });
      } else {
        const text = await result.text();
        let decoded: unknown = text;
        try { decoded = text ? JSON.parse(text) : null; } catch { /* Preserve plain text. */ }
        setResponse({ ...base, duration_ms: Math.round(performance.now() - started), body: decoded });
      }
    } catch (reason) {
      if (reason instanceof DOMException && reason.name === "AbortError") {
        setError(locale === "zh" ? "请求已终止。" : "Request aborted.");
      } else {
        setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
      }
    } finally {
      window.clearInterval(timer);
      setElapsed(Math.round(performance.now() - started));
      setRunning(false);
      controller.current = null;
    }
  }

  function curlCommand() {
    const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
    const parts = [`curl ${quote(endpoint)}`, `-X ${method}`];
    if (apiKey.trim()) parts.push(`-H ${quote(`Authorization: Bearer ${apiKey.trim()}`)}`);
    if (method !== "GET") {
      if (mode === "multipart") {
        try {
          for (const [key, value] of Object.entries(parsePayload())) parts.push(`-F ${quote(`${key}=${typeof value === "object" ? JSON.stringify(value) : String(value)}`)}`);
        } catch { /* Inline validation reports malformed JSON. */ }
        if (file) parts.push(`-F ${quote("image=@/path/to/" + file.name)}`);
      } else {
        let payload = body;
        try { payload = JSON.stringify(parsePayload()); } catch { /* Preserve the editor text for copy. */ }
        parts.push(`-H ${quote("Content-Type: application/json")}`, `--data-raw ${quote(payload)}`);
      }
    }
    if (streamSupported && stream) parts.push("--no-buffer");
    return parts.join(" \\\n  ");
  }

  async function copy(value: string, message: string) {
    await navigator.clipboard.writeText(value);
    toast(message);
  }

  const responseText = typeof response?.body === "string" ? response.body : JSON.stringify(response?.body ?? {}, null, 2);
  const responseOK = Boolean(response && response.status < 400);

  return (
    <ContentFrame>
      <PageHeader title={t("debugger.title")} description={t("debugger.description")} actions={<><Button size="small" variant="secondary" onClick={() => void copy(curlCommand(), locale === "zh" ? "cURL 已复制" : "cURL copied")}><Copy className="size-3.5" />cURL</Button><IconButton label={locale === "zh" ? "重置请求" : "Reset request"} variant="tertiary" onClick={reset}><RotateCcw className="size-4" /></IconButton></>} />
      <div className="grid border-y border-border bg-surface 2xl:grid-cols-2">
        <section aria-labelledby="request-heading" className="min-w-0 border-b border-border 2xl:border-b-0 2xl:border-r">
          <div className="flex h-11 items-center justify-between border-b border-border px-4 sm:px-6"><div className="flex items-center gap-2"><span className="size-2 rounded-full bg-blue" /><h2 id="request-heading" className="text-label-14 font-semibold">{locale === "zh" ? "请求" : "Request"}</h2></div><Badge tone="blue">{method}</Badge></div>
          <div className="grid gap-4 p-4 sm:p-6">
            <div className="grid gap-3 sm:grid-cols-[112px_minmax(0,1fr)]"><Select aria-label="Method" value={method} onChange={(event) => changeMethod(event.target.value)} options={[{ value: "POST", label: "POST" }, { value: "GET", label: "GET" }]} /><Select aria-label="Endpoint" value={endpoint} onChange={(event) => changeEndpoint(event.target.value)} options={Object.keys(examples).map((value) => ({ value, label: value }))} /></div>
            <Tabs value={mode} onValueChange={(value) => setMode(value as RequestMode)}><TabsList aria-label={locale === "zh" ? "正文格式" : "Body format"}><TabsTrigger value="json">JSON</TabsTrigger><TabsTrigger value="multipart" disabled={method === "GET"}>Multipart</TabsTrigger></TabsList></Tabs>
            <Input type="password" label={locale === "zh" ? "客户端密钥" : "Client API Key"} value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder="gg_live_..." autoComplete="off" description={locale === "zh" ? "仅用于本次浏览器请求，不会保存。" : "Used only for this browser request and never saved."} />
            <Switch checked={stream} onCheckedChange={setStream} disabled={!streamSupported} label={locale === "zh" ? "流式响应" : "Stream Response"} description={streamSupported ? (locale === "zh" ? "以 SSE 事件实时展示响应。" : "Render the response as live SSE events.") : (locale === "zh" ? "当前端点不支持流式输出。" : "The selected endpoint does not stream.")} />
            {mode === "multipart" ? <Input type="file" label={locale === "zh" ? "图片文件" : "Image File"} accept="image/*" onChange={(event) => setFile(event.target.files?.[0] ?? null)} prefix={<FileUp className="size-3.5" />} /> : null}
            <EditorFrame label={mode === "json" ? "JSON" : "Multipart fields (JSON)"}><CodeMirror value={body} height="320px" theme="dark" extensions={jsonExtension} onChange={setBody} basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: true }} /></EditorFrame>
            {error ? <p role="alert" className="flex items-start gap-2 rounded-[6px] border border-red-soft bg-red-soft p-3 text-copy-13 text-danger"><CircleAlert className="mt-0.5 size-4 shrink-0" />{error}</p> : null}
            <div className="flex min-h-9 flex-wrap items-center gap-2">{running ? <Button variant="danger" onClick={() => controller.current?.abort()}><Square className="size-3.5" />{locale === "zh" ? "终止请求" : "Stop Request"}</Button> : <Button onClick={() => void run()}><Play className="size-3.5" />{locale === "zh" ? "发送请求" : "Send Request"}</Button>}<span className="ml-auto flex items-center gap-1.5 text-copy-13 tabular-nums text-fg-muted"><Clock3 className="size-3.5" />{elapsed} ms</span></div>
          </div>
        </section>

        <section aria-labelledby="response-heading" className="min-w-0">
          <div className="flex min-h-11 flex-wrap items-center gap-2 border-b border-border px-4 py-1.5 sm:px-6"><div className="flex items-center gap-2"><span className={`size-2 rounded-full ${response ? (responseOK ? "bg-green" : "bg-danger") : "bg-gray-300"}`} /><h2 id="response-heading" className="text-label-14 font-semibold">{locale === "zh" ? "响应" : "Response"}</h2></div>{response ? <><Badge tone={responseOK ? "green" : "red"}>{response.status}</Badge>{response.streaming ? <Badge tone="blue">SSE</Badge> : null}<span className="ml-auto flex items-center gap-1.5 text-copy-13 tabular-nums text-fg-muted"><Clock3 className="size-3.5" />{response.duration_ms} ms</span><IconButton label={locale === "zh" ? "复制响应" : "Copy response"} variant="tertiary" onClick={() => void copy(responseText, t("common.copied"))}><Copy className="size-4" /></IconButton></> : null}</div>
          {response ? <div className="grid gap-4 p-4 sm:p-6"><div className={`flex items-center gap-2 text-copy-13 ${responseOK ? "text-green" : "text-danger"}`}>{responseOK ? <Check className="size-4" /> : <CircleAlert className="size-4" />}{response.streaming ? (locale === "zh" ? "正在接收事件" : "Receiving events") : responseOK ? (locale === "zh" ? "请求已完成" : "Request completed") : (locale === "zh" ? "上游返回错误" : "Request returned an error")}</div><details className="rounded-[6px] border border-border bg-subtle px-3 py-2"><summary className="cursor-pointer text-label-13 font-medium outline-none focus-visible:shadow-focus">{locale === "zh" ? "响应头" : "Response Headers"}</summary><pre className="scrollbar mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-copy-13 text-fg-muted">{Object.entries(response.headers).map(([key, value]) => `${key}: ${value}`).join("\n")}</pre></details><EditorFrame label={response.streaming ? "SSE Events" : "Response"}><CodeMirror value={responseText} height="420px" theme="dark" extensions={response.streaming ? [] : jsonExtension} editable={false} basicSetup={{ lineNumbers: true, foldGutter: true }} /></EditorFrame></div> : <div className="grid min-h-[420px] place-items-center p-6 text-center sm:min-h-[552px]"><div className="max-w-xs"><div className="mx-auto grid size-10 place-items-center rounded-[7px] border border-border bg-subtle shadow-control"><Play className="size-4 text-fg-muted" /></div><h3 className="mt-3 text-heading-16 font-semibold">{locale === "zh" ? "等待请求" : "Waiting for a request"}</h3><p className="mt-1 text-copy-13 text-fg-muted">{locale === "zh" ? "发送请求后，状态、响应头、事件和耗时会显示在这里。" : "Status, headers, events, and timing appear here after sending."}</p></div></div>}
        </section>
      </div>
    </ContentFrame>
  );
}

function EditorFrame({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="overflow-hidden rounded-[7px] border border-[#30363d] bg-[#0d1117] shadow-control"><div className="flex h-9 items-center border-b border-white/10 px-3 font-mono text-[11px] text-white/60">{label}</div><div className="min-w-0">{children}</div></div>;
}
