import type { components, operations, paths } from "./api-types";

declare global {
  interface Window {
    __API_BASE_URL__?: string;
    __SERVER_PORT__?: number | string;
  }
}

export type APIPaths = paths;
export type APIComponents = components;
export type APIOperationID = keyof operations;
export type APISchema<Name extends keyof components["schemas"]> = components["schemas"][Name];
export type APIErrorBody = APISchema<"ErrorResponse">;

type JSONContent<T> = T extends { content: { "application/json": infer Body } } ? Body : never;
type JSONRequestBody<ID extends APIOperationID> = operations[ID] extends { requestBody: infer Body } ? JSONContent<Body> : never;
type JSONResponseBody<
  ID extends APIOperationID,
  Status extends keyof operations[ID]["responses"],
> = operations[ID]["responses"][Status] extends { content: { "application/json": infer Body } } ? Body : void;
type QueryParameters<ID extends APIOperationID> = operations[ID]["parameters"] extends { query?: infer Query } ? Query : never;
type SuccessStatus<ID extends APIOperationID> = Extract<keyof operations[ID]["responses"], 200 | 201 | 202 | 204>;

export type APIRequest<ID extends APIOperationID> = JSONRequestBody<ID>;
export type APIQuery<ID extends APIOperationID> = QueryParameters<ID>;
export type APIResponse<
  ID extends APIOperationID,
  Status extends keyof operations[ID]["responses"] = SuccessStatus<ID>,
> = JSONResponseBody<ID, Status>;
export type GenerationEvent = APISchema<"GenerationEvent">;

let apiBaseURL = defaultAPIBaseURL();
let accessToken: string | null = null;
let refreshPromise: Promise<boolean> | null = null;
let authCallbacks: AuthCallbacks = {};

interface AuthCallbacks {
  onRefresh?: (response: APIResponse<"refresh", 200>) => void;
  onAuthenticationLost?: () => void;
}

export function configureAPIBaseURL(url: string) {
  apiBaseURL = trimTrailingSlash(url);
}

export function getAPIBaseURL(): string {
  return apiBaseURL;
}

export function setAccessToken(token: string | null) {
  accessToken = token;
}

export function getAccessToken(): string | null {
  return accessToken;
}

export function configureAuthCallbacks(callbacks: AuthCallbacks) {
  authCallbacks = callbacks;
}

export class APIError extends Error {
  readonly status: number;
  readonly body: APIErrorBody | null;

  constructor(response: Response, body: APIErrorBody | null) {
    super(body?.error || `API request failed with status ${response.status}`);
    this.name = "APIError";
    this.status = response.status;
    this.body = body;
  }
}

export async function ping(): Promise<APIResponse<"ping", 200>> {
  return apiFetch<APIResponse<"ping", 200>>("/ping");
}

export async function listLanguages(): Promise<APIResponse<"listLanguages", 200>> {
  return apiFetch<APIResponse<"listLanguages", 200>>("/languages");
}

export async function listLLMModels(): Promise<APIResponse<"listLLMModels", 200>> {
  return apiFetch<APIResponse<"listLLMModels", 200>>("/llm/models");
}

export type Conversation = APIResponse<"getConversation", 200>;
export type ConversationSummary = APIResponse<"listConversations", 200>["conversations"][number];

export async function listConversations(): Promise<APIResponse<"listConversations", 200>> {
  return apiFetch<APIResponse<"listConversations", 200>>("/conversations");
}

export async function startConversation(
  request: APIRequest<"startConversation"> = {},
): Promise<APIResponse<"startConversation", 201>> {
  return apiFetch<APIResponse<"startConversation", 201>>(
    "/conversations",
    jsonRequest("POST", request),
  );
}

export async function getConversation(conversationID: string): Promise<Conversation> {
  return apiFetch<Conversation>(`/conversations/${encodeURIComponent(conversationID)}`);
}

export async function respondToConversation(
  conversationID: string,
  request: APIRequest<"respondToConversation">,
): Promise<APIResponse<"respondToConversation", 200>> {
  return apiFetch<APIResponse<"respondToConversation", 200>>(
    `/conversations/${encodeURIComponent(conversationID)}/respond`,
    jsonRequest("POST", request),
  );
}

export async function respondToConversationAudio(
  conversationID: string,
  audio: Blob,
): Promise<APIResponse<"respondToConversationAudio", 200>> {
  const form = new FormData();
  form.set("file", new File([audio], conversationAudioFilename(audio.type), {
    type: audio.type || "application/octet-stream",
  }));
  return apiFetch<APIResponse<"respondToConversationAudio", 200>>(
    `/conversations/${encodeURIComponent(conversationID)}/respond/audio`,
    { method: "POST", body: form },
  );
}

export async function transcribeConversationAudio(
  conversationID: string,
  audio: Blob,
): Promise<APIResponse<"transcribeConversationAudio", 200>> {
  const form = new FormData();
  form.set("file", new File([audio], conversationAudioFilename(audio.type), {
    type: audio.type || "application/octet-stream",
  }));
  return apiFetch<APIResponse<"transcribeConversationAudio", 200>>(
    `/conversations/${encodeURIComponent(conversationID)}/transcribe`,
    { method: "POST", body: form },
  );
}

export async function getConversationTurnAudio(audioURL: string): Promise<Blob> {
  return getAuthenticatedAudio(audioURL);
}

export async function getStorySentenceAudio(
  storyID: string,
  position: number,
  voiceModel = "default",
  cache?: RequestCache,
): Promise<Blob> {
  return getAuthenticatedAudio(
    `/stories/${encodeURIComponent(storyID)}/sentences/${position}/audio?voice_model=${encodeURIComponent(voiceModel)}`,
    cache,
  );
}

export async function getStorySentenceAlignment(
  storyID: string,
  position: number,
  voiceModel = "default",
): Promise<APIResponse<"getStorySentenceAlignment", 200>> {
  return apiFetch<APIResponse<"getStorySentenceAlignment", 200>>(
    `/stories/${encodeURIComponent(storyID)}/sentences/${position}/alignment?voice_model=${encodeURIComponent(voiceModel)}`,
  );
}

async function getAuthenticatedAudio(audioURL: string, cache?: RequestCache): Promise<Blob> {
  const request: RequestInit = { headers: { Accept: "audio/mpeg" }, cache };
  let response = await sendRequest(audioURL, request);
  if (response.status === 401 && await refreshAccessToken()) {
    response = await sendRequest(audioURL, request);
  }
  if (response.status === 401) {
    setAccessToken(null);
    authCallbacks.onAuthenticationLost?.();
  }
  if (!response.ok) {
    const body = parseJSON(await response.text());
    throw new APIError(response, isErrorBody(body) ? body : null);
  }
  return response.blob();
}

export async function register(request: APIRequest<"register">): Promise<APIResponse<"register", 201>> {
  return apiFetch<APIResponse<"register", 201>>("/auth/register", jsonRequest("POST", request), false);
}

export async function login(request: APIRequest<"login">): Promise<APIResponse<"login", 200>> {
  return apiFetch<APIResponse<"login", 200>>("/auth/login", jsonRequest("POST", request), false);
}

export async function refresh(): Promise<APIResponse<"refresh", 200>> {
  return apiFetch<APIResponse<"refresh", 200>>("/auth/refresh", { method: "POST" }, false);
}

export async function logout(): Promise<void> {
  return apiFetch<void>("/auth/logout", { method: "POST" }, false);
}

export async function logoutAll(): Promise<void> {
  return apiFetch<void>("/auth/logout-all", { method: "POST" });
}

export async function me(): Promise<APIResponse<"me", 200>> {
  return apiFetch<APIResponse<"me", 200>>("/auth/me");
}

export async function getProfile(): Promise<APIResponse<"getProfile", 200>> {
  return apiFetch<APIResponse<"getProfile", 200>>("/profile");
}

export async function patchProfile(request: APIRequest<"patchProfile">): Promise<APIResponse<"patchProfile", 200>> {
  return apiFetch<APIResponse<"patchProfile", 200>>("/profile", jsonRequest("PATCH", request));
}

export async function listSkills(query: APIQuery<"listSkills"> = {}): Promise<APIResponse<"listSkills", 200>> {
  return apiFetch<APIResponse<"listSkills", 200>>(`/skills${queryString(query)}`);
}

export async function listSessions(query: APIQuery<"listSessions"> = {}): Promise<APIResponse<"listSessions", 200>> {
  return apiFetch<APIResponse<"listSessions", 200>>(`/sessions${queryString(query)}`);
}

export async function generateSession(request: APIRequest<"generateSession">): Promise<APIResponse<"generateSession", 202>> {
  return apiFetch<APIResponse<"generateSession", 202>>(
    "/sessions/generate",
    jsonRequest("POST", request),
  );
}

export async function importStory(request: APIRequest<"importStory">): Promise<APIResponse<"importStory", 201>> {
  return apiFetch<APIResponse<"importStory", 201>>(
    "/stories/import",
    jsonRequest("POST", request),
  );
}

export async function listImportedStories(query: APIQuery<"listImportedStories"> = {}): Promise<APIResponse<"listImportedStories", 200>> {
  return apiFetch<APIResponse<"listImportedStories", 200>>(`/stories${queryString(query)}`);
}

export async function deleteStory(storyID: string): Promise<void> {
  return apiFetch<void>(`/stories/${encodeURIComponent(storyID)}`, { method: "DELETE" });
}

export async function generateStoryTasks(storyID: string): Promise<APIResponse<"generateStoryTasks", 202>> {
  return apiFetch<APIResponse<"generateStoryTasks", 202>>(
    `/stories/${encodeURIComponent(storyID)}/tasks/generate`,
    { method: "POST" },
  );
}

export interface ImportStoryFileRequest {
  language?: string;
  level?: string;
  title?: string;
  file: File;
}

interface APIRequestOptions {
  signal?: AbortSignal;
}

export async function importStoryFile(request: ImportStoryFileRequest): Promise<APIResponse<"importStory", 201>> {
  const form = new FormData();
  form.set("file", request.file);
  if (request.language) {
    form.set("language", request.language);
  }
  if (request.level) {
    form.set("level", request.level);
  }
  if (request.title) {
    form.set("title", request.title);
  }
  return apiFetch<APIResponse<"importStory", 201>>(
    "/stories/import",
    { method: "POST", body: form },
  );
}

export async function getSessionDetail(
  sessionID: string,
  options: APIRequestOptions = {},
): Promise<APIResponse<"getSessionDetail", 200>> {
  return apiFetch<APIResponse<"getSessionDetail", 200>>(
    `/sessions/${encodeURIComponent(sessionID)}`,
    { signal: options.signal },
  );
}

export async function getSessionDebug(sessionID: string): Promise<APIResponse<"getSessionDebug", 200>> {
  return apiFetch<APIResponse<"getSessionDebug", 200>>(`/sessions/${encodeURIComponent(sessionID)}/debug`);
}

export async function archiveSession(sessionID: string): Promise<void> {
  return apiFetch<void>(`/sessions/${encodeURIComponent(sessionID)}/archive`, { method: "POST" });
}

export async function unarchiveSession(sessionID: string): Promise<void> {
  return apiFetch<void>(`/sessions/${encodeURIComponent(sessionID)}/archive`, { method: "DELETE" });
}

export async function deleteSession(sessionID: string): Promise<void> {
  return apiFetch<void>(`/sessions/${encodeURIComponent(sessionID)}`, { method: "DELETE" });
}

export async function getSessionContent(
  sessionID: string,
  options: APIRequestOptions = {},
): Promise<APIResponse<"getSessionContent", 200>> {
  return apiFetch<APIResponse<"getSessionContent", 200>>(
    `/sessions/${encodeURIComponent(sessionID)}/content`,
    { signal: options.signal },
  );
}

export async function recordTargetPreviewGuess(
  sessionID: string,
  request: APIRequest<"recordTargetPreviewGuess">,
): Promise<APIResponse<"recordTargetPreviewGuess", 200>> {
  return apiFetch<APIResponse<"recordTargetPreviewGuess", 200>>(
    `/sessions/${encodeURIComponent(sessionID)}/target-preview/guesses`,
    jsonRequest("POST", request),
  );
}

export async function retrySession(sessionID: string): Promise<APIResponse<"retrySession", 202>> {
  return apiFetch<APIResponse<"retrySession", 202>>(
    `/sessions/${encodeURIComponent(sessionID)}/retry`,
    { method: "POST" },
  );
}

export async function startReading(sessionID: string): Promise<APIResponse<"startReading", 204>> {
  return apiFetch<APIResponse<"startReading", 204>>(
    `/sessions/${encodeURIComponent(sessionID)}/reading`,
    { method: "POST" },
  );
}

export async function completeSession(sessionID: string): Promise<APIResponse<"completeSession", 204>> {
  return apiFetch<APIResponse<"completeSession", 204>>(
    `/sessions/${encodeURIComponent(sessionID)}/complete`,
    { method: "POST" },
  );
}

export interface GenerationStreamHandlers {
  /** Called for each parsed SSE generation event, including the terminal stage="done". */
  onEvent: (event: GenerationEvent) => void;
  /** Called when the stream cannot be opened or fails mid-flight (not on clean close). */
  onError?: (error: unknown) => void;
  /** Called when the server closes the stream (generation finished). */
  onClose?: () => void;
}

// streamGenerationEvents consumes GET /sessions/{id}/events as Server-Sent
// Events. The native EventSource API cannot attach the Authorization header, so
// generation progress is read from a streamed fetch response instead — which also
// lets a 401 trigger a single refresh-and-reconnect. Returns a cleanup function
// that aborts the in-flight request; callers invoke it from onCleanup.
export function streamGenerationEvents(sessionID: string, handlers: GenerationStreamHandlers): () => void {
  const controller = new AbortController();
  void consumeGenerationStream(sessionID, handlers, controller.signal, true);
  return () => controller.abort();
}

async function consumeGenerationStream(
  sessionID: string,
  handlers: GenerationStreamHandlers,
  signal: AbortSignal,
  allowRefresh: boolean,
): Promise<void> {
  const path = `/sessions/${encodeURIComponent(sessionID)}/events`;
  let response: Response;
  try {
    response = await sendEventStreamRequest(path, signal);
  } catch (error) {
    if (!signal.aborted) {
      handlers.onError?.(error);
    }
    return;
  }

  if (response.status === 401 && allowRefresh) {
    if (await refreshAccessToken()) {
      return consumeGenerationStream(sessionID, handlers, signal, false);
    }
    setAccessToken(null);
    authCallbacks.onAuthenticationLost?.();
    handlers.onError?.(new APIError(response, null));
    return;
  }
  if (!response.ok || !response.body) {
    handlers.onError?.(new APIError(response, null));
    return;
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      let separator = buffer.indexOf("\n\n");
      while (separator !== -1) {
        const frame = buffer.slice(0, separator);
        buffer = buffer.slice(separator + 2);
        const event = parseSSEFrame(frame);
        if (event) {
          handlers.onEvent(event);
        }
        separator = buffer.indexOf("\n\n");
      }
    }
  } catch (error) {
    if (!signal.aborted) {
      handlers.onError?.(error);
    }
    return;
  } finally {
    reader.releaseLock();
  }

  if (!signal.aborted) {
    handlers.onClose?.();
  }
}

function sendEventStreamRequest(path: string, signal: AbortSignal): Promise<Response> {
  const headers = new Headers({ Accept: "text/event-stream" });
  if (accessToken) {
    headers.set("Authorization", `Bearer ${accessToken}`);
  }
  return fetch(apiURL(path), { method: "GET", credentials: "include", headers, signal });
}

// parseSSEFrame extracts the JSON payload from one SSE frame. Comment keepalive
// frames (": keepalive") carry no data line and are skipped.
function parseSSEFrame(frame: string): GenerationEvent | null {
  const dataLines: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).replace(/^ /, ""));
    }
  }
  if (dataLines.length === 0) {
    return null;
  }
  try {
    return JSON.parse(dataLines.join("\n")) as GenerationEvent;
  } catch {
    return null;
  }
}

export async function getStory(
  storyID: string,
  options: APIRequestOptions = {},
): Promise<APIResponse<"getStory", 200>> {
  return apiFetch<APIResponse<"getStory", 200>>(
    `/stories/${encodeURIComponent(storyID)}`,
    { signal: options.signal },
  );
}

export async function getDefinition(storyID: string, key: string): Promise<APIResponse<"getDefinition", 200>> {
  return apiFetch<APIResponse<"getDefinition", 200>>(
    `/stories/${encodeURIComponent(storyID)}/definition?${new URLSearchParams({ key })}`,
  );
}

export async function getDefinitionOptions(
  storyID: string,
  key: string,
): Promise<APIResponse<"getDefinitionOptions", 200>> {
  return apiFetch<APIResponse<"getDefinitionOptions", 200>>(
    `/stories/${encodeURIComponent(storyID)}/definition/options?key=${encodeURIComponent(key)}`,
  );
}

export async function getDictionaryEntry(
  language: string,
  key: string,
): Promise<APIResponse<"getDictionaryEntry", 200>> {
  return apiFetch<APIResponse<"getDictionaryEntry", 200>>(
    `/dictionary/entry?${new URLSearchParams({ language, key })}`,
  );
}

export async function putDictionaryEntry(
  request: APIRequest<"putDictionaryEntry">,
): Promise<APIResponse<"putDictionaryEntry", 200>> {
  return apiFetch<APIResponse<"putDictionaryEntry", 200>>("/dictionary/entry", jsonRequest("PUT", request));
}

export async function deleteDictionaryEntry(language: string, key: string): Promise<void> {
  await apiFetch<void>(`/dictionary/entry?${new URLSearchParams({ language, key })}`, { method: "DELETE" });
}

export async function sentenceBreakdown(
  storyID: string,
  request: APIRequest<"postSentenceBreakdown">,
): Promise<APIResponse<"postSentenceBreakdown", 200>> {
  return apiFetch<APIResponse<"postSentenceBreakdown", 200>>(
    `/stories/${encodeURIComponent(storyID)}/sentence`,
    jsonRequest("POST", request),
  );
}

export async function wordBreakdown(
  storyID: string,
  request: APIRequest<"postWordBreakdown">,
): Promise<APIResponse<"postWordBreakdown", 200>> {
  return apiFetch<APIResponse<"postWordBreakdown", 200>>(
    `/stories/${encodeURIComponent(storyID)}/word`,
    jsonRequest("POST", request),
  );
}

export async function postReaderEvents(
  request: APIRequest<"postReaderEvents">,
  options: { keepalive?: boolean } = {},
): Promise<APIResponse<"postReaderEvents", 202>> {
  const init = jsonRequest("POST", request);
  if (options.keepalive) {
    // Lets the batch survive a flush fired from visibilitychange/beforeunload.
    init.keepalive = true;
  }
  return apiFetch<APIResponse<"postReaderEvents", 202>>("/reader/events", init, !options.keepalive);
}

export async function setReaderSurfaceKnowledge(request: APIRequest<"putReaderSurfaceKnowledge">): Promise<void> {
  return apiFetch<void>("/reader/surface_knowledge", jsonRequest("PUT", request));
}

export async function setWordKnowledge(token: string, request: APIRequest<"putWordKnowledge">): Promise<void> {
  return apiFetch<void>(`/word_knowledge/${encodeURIComponent(token)}`, jsonRequest("PUT", request));
}

export async function getSessionTasks(
  sessionID: string,
  options: APIRequestOptions = {},
): Promise<APIResponse<"getSessionTasks", 200>> {
  return apiFetch<APIResponse<"getSessionTasks", 200>>(
    `/sessions/${encodeURIComponent(sessionID)}/tasks`,
    { signal: options.signal },
  );
}

export async function getTask(taskID: string): Promise<APIResponse<"getTask", 200>> {
  return apiFetch<APIResponse<"getTask", 200>>(`/tasks/${encodeURIComponent(taskID)}`);
}

export async function submitTask(taskID: string, request: APIRequest<"submitTask">): Promise<APIResponse<"submitTask", 200>> {
  return apiFetch<APIResponse<"submitTask", 200>>(
    `/tasks/${encodeURIComponent(taskID)}/submit`,
    jsonRequest("POST", request),
  );
}

export async function reportTask(taskID: string, request: APIRequest<"reportTask">): Promise<APISchema<"TaskReportResponse">> {
  const path = `/tasks/${encodeURIComponent(taskID)}/report`;
  const init = jsonRequest("POST", request);
  let response = await sendRequest(path, init);
  if (response.status === 401) {
    if (await refreshAccessToken()) {
      response = await sendRequest(path, init);
    }
    if (response.status === 401) {
      setAccessToken(null);
      authCallbacks.onAuthenticationLost?.();
    }
  }
  if (response.status === 503) {
    const body = parseJSON(await response.text());
    if (isTaskReportResponse(body)) {
      return body;
    }
    throw new APIError(response, isErrorBody(body) ? body : null);
  }
  return decodeResponse<APISchema<"TaskReportResponse">>(response);
}

// Admin/observability surface (#24). Every /admin route returns 404 for
// non-admins, so getAdminContext doubles as the admin-flag probe: a 200 means
// admin, an APIError with status 404 means not.
export async function getAdminContext(): Promise<APIResponse<"getAdminContext", 200>> {
  return apiFetch<APIResponse<"getAdminContext", 200>>("/admin/context");
}

export async function adminListCalls(query: APIQuery<"adminListCalls"> = {}): Promise<APIResponse<"adminListCalls", 200>> {
  return apiFetch<APIResponse<"adminListCalls", 200>>(`/admin/calls${queryString(query ?? {})}`);
}

export async function adminGetCall(callID: string): Promise<APIResponse<"adminGetCall", 200>> {
  return apiFetch<APIResponse<"adminGetCall", 200>>(`/admin/calls/${encodeURIComponent(callID)}`);
}

export async function adminGetSession(sessionID: string): Promise<APIResponse<"adminGetSession", 200>> {
  return apiFetch<APIResponse<"adminGetSession", 200>>(`/admin/sessions/${encodeURIComponent(sessionID)}`);
}

export async function adminGetUser(idOrEmail: string): Promise<APIResponse<"adminGetUser", 200>> {
  return apiFetch<APIResponse<"adminGetUser", 200>>(`/admin/users/${encodeURIComponent(idOrEmail)}`);
}

export async function adminCostRollup(query: APIQuery<"adminCostRollup"> = {}): Promise<APIResponse<"adminCostRollup", 200>> {
  return apiFetch<APIResponse<"adminCostRollup", 200>>(`/admin/cost${queryString(query ?? {})}`);
}

function jsonRequest(method: "PATCH" | "POST" | "PUT", body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

function queryString(params: object): string {
  const out = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null) {
      out.set(key, String(value));
    }
  }
  const encoded = out.toString();
  return encoded ? `?${encoded}` : "";
}

async function apiFetch<T>(path: string, init: RequestInit = {}, retryUnauthorized = true): Promise<T> {
  let response = await sendRequest(path, init);
  if (response.status === 401 && retryUnauthorized) {
    if (await refreshAccessToken()) {
      response = await sendRequest(path, init);
    }
    if (response.status === 401) {
      setAccessToken(null);
      authCallbacks.onAuthenticationLost?.();
    }
  }
  return decodeResponse<T>(response);
}

async function sendRequest(path: string, init: RequestInit, includeAccessToken = true): Promise<Response> {
  const headers = new Headers(init.headers);
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json");
  }
  if (includeAccessToken && accessToken) {
    headers.set("Authorization", `Bearer ${accessToken}`);
  }

  return fetch(apiURL(path), {
    ...init,
    credentials: "include",
    headers,
  });
}

function conversationAudioFilename(contentType: string): string {
  if (contentType.includes("mp4")) return "answer.m4a";
  if (contentType.includes("ogg")) return "answer.ogg";
  if (contentType.includes("wav")) return "answer.wav";
  return "answer.webm";
}

async function decodeResponse<T>(response: Response): Promise<T> {
  if (response.status === 204) {
    if (!response.ok) {
      throw new APIError(response, null);
    }
    return undefined as T;
  }

  const text = await response.text();
  const body = parseJSON(text);
  if (!response.ok) {
    throw new APIError(response, isErrorBody(body) ? body : null);
  }
  return body as T;
}

async function refreshAccessToken(): Promise<boolean> {
  if (refreshPromise) {
    return refreshPromise;
  }
  refreshPromise = (async () => {
    const response = await sendRequest("/auth/refresh", { method: "POST" }, false);
    if (!response.ok) {
      return false;
    }
    const body = await decodeResponse<APIResponse<"refresh", 200>>(response);
    setAccessToken(body.access_token);
    authCallbacks.onRefresh?.(body);
    return true;
  })().catch(() => false).finally(() => {
    refreshPromise = null;
  });
  return refreshPromise;
}

function apiURL(path: string): string {
  if (/^https?:\/\//.test(path)) {
    return path;
  }
  return `${apiBaseURL}${path.startsWith("/") ? path : `/${path}`}`;
}

function defaultAPIBaseURL(): string {
  if (typeof window === "undefined") {
    return "/api/v1";
  }
  if (window.__API_BASE_URL__) {
    return trimTrailingSlash(window.__API_BASE_URL__);
  }
  if (window.__SERVER_PORT__) {
    return `http://127.0.0.1:${window.__SERVER_PORT__}/api/v1`;
  }
  return "/api/v1";
}

function trimTrailingSlash(url: string): string {
  return url.replace(/\/+$/, "");
}

function parseJSON(text: string): unknown {
  if (!text) {
    return null;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return null;
  }
}

function isErrorBody(body: unknown): body is APIErrorBody {
  return typeof body === "object" && body !== null && "error" in body && typeof (body as { error: unknown }).error === "string";
}

function isTaskReportResponse(body: unknown): body is APISchema<"TaskReportResponse"> {
  const candidate = body as Partial<APISchema<"TaskReportResponse">> | null;
  return typeof candidate === "object" && candidate !== null &&
    typeof candidate.report_id === "string" &&
    typeof candidate.task_id === "string" &&
    typeof candidate.status === "string" &&
    typeof candidate.message === "string";
}
