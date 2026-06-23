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

export async function register(request: APIRequest<"register">): Promise<APIResponse<"register", 201>> {
  return apiFetch<APIResponse<"register", 201>>("/auth/register", jsonRequest("POST", request));
}

export async function login(request: APIRequest<"login">): Promise<APIResponse<"login", 200>> {
  return apiFetch<APIResponse<"login", 200>>("/auth/login", jsonRequest("POST", request));
}

export async function refresh(): Promise<APIResponse<"refresh", 200>> {
  return apiFetch<APIResponse<"refresh", 200>>("/auth/refresh", { method: "POST" });
}

export async function logout(): Promise<void> {
  return apiFetch<void>("/auth/logout", { method: "POST" });
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

export async function listSessions(query: APIQuery<"listSessions"> = {}): Promise<APIResponse<"listSessions", 200>> {
  return apiFetch<APIResponse<"listSessions", 200>>(`/sessions${queryString(query)}`);
}

export async function generateSession(request: APIRequest<"generateSession">): Promise<APIResponse<"generateSession", 202>> {
  return apiFetch<APIResponse<"generateSession", 202>>(
    "/sessions/generate",
    jsonRequest("POST", request),
  );
}

export async function getSessionDetail(sessionID: string): Promise<APIResponse<"getSessionDetail", 200>> {
  return apiFetch<APIResponse<"getSessionDetail", 200>>(`/sessions/${encodeURIComponent(sessionID)}`);
}

export async function retrySession(sessionID: string): Promise<APIResponse<"retrySession", 202>> {
  return apiFetch<APIResponse<"retrySession", 202>>(
    `/sessions/${encodeURIComponent(sessionID)}/retry`,
    { method: "POST" },
  );
}

export function sessionEventsURL(sessionID: string): string {
  return apiURL(`/sessions/${encodeURIComponent(sessionID)}/events`);
}

export function parseGenerationEvent(data: string): GenerationEvent {
  return JSON.parse(data) as GenerationEvent;
}

export async function getStory(storyID: string): Promise<APIResponse<"getStory", 200>> {
  return apiFetch<APIResponse<"getStory", 200>>(`/stories/${encodeURIComponent(storyID)}`);
}

export async function getDefinition(storyID: string, key: string): Promise<APIResponse<"getDefinition", 200>> {
  return apiFetch<APIResponse<"getDefinition", 200>>(
    `/stories/${encodeURIComponent(storyID)}/definition?${new URLSearchParams({ key })}`,
  );
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

export async function postReaderEvents(request: APIRequest<"postReaderEvents">): Promise<APIResponse<"postReaderEvents", 202>> {
  return apiFetch<APIResponse<"postReaderEvents", 202>>(
    "/reader/events",
    jsonRequest("POST", request),
  );
}

export async function setWordKnowledge(token: string, request: APIRequest<"putWordKnowledge">): Promise<void> {
  return apiFetch<void>(`/word_knowledge/${encodeURIComponent(token)}`, jsonRequest("PUT", request));
}

export async function getSessionTasks(sessionID: string): Promise<APIResponse<"getSessionTasks", 200>> {
  return apiFetch<APIResponse<"getSessionTasks", 200>>(`/sessions/${encodeURIComponent(sessionID)}/tasks`);
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

async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (accessToken) {
    headers.set("Authorization", `Bearer ${accessToken}`);
  }

  const response = await fetch(apiURL(path), {
    ...init,
    credentials: "include",
    headers,
  });

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
