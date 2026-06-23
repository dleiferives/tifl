import { createMemo, createSignal } from "solid-js";
import { configureAuthCallbacks, setAccessToken, type APISchema } from "./api";

export type AuthStatus = "checking" | "anonymous" | "authenticated" | "local";
export type ToastKind = "error" | "info";

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

type User = APISchema<"User">;
type AuthResponse = APISchema<"AuthResponse">;
type Profile = APISchema<"Profile">;
type Level = Profile["level"];

const [authStatus, setAuthStatus] = createSignal<AuthStatus>("checking");
const [user, setUser] = createSignal<User | null>(null);
const [profile, setProfileSignal] = createSignal<Profile | null>(null);
const [authLoading, setAuthLoading] = createSignal(false);
const [authError, setAuthError] = createSignal("");
const [returnPath, setReturnPath] = createSignal("/");
const [activeLanguage, setActiveLanguage] = createSignal("");
const [currentLevel, setCurrentLevel] = createSignal<Level>("beginner");
const [pendingOperations, setPendingOperations] = createSignal(0);
const [toast, setToast] = createSignal<Toast | null>(null);
let nextToastID = 1;

function setAuthenticated(response: AuthResponse) {
  setAccessToken(response.access_token);
  setUser(response.user);
  setAuthError("");
  setAuthStatus("authenticated");
}

function clearAuthentication() {
  setAccessToken(null);
  setUser(null);
  setProfile(null);
  setAuthLoading(false);
  setAuthStatus("anonymous");
}

function setLocalMode(next: Profile) {
  setAccessToken(null);
  setUser(null);
  setProfile(next);
  setAuthError("");
  setAuthStatus("local");
}

function setProfile(next: Profile | null) {
  setProfileSignal(next);
  if (next) {
    setActiveLanguage(next.active_language);
    setCurrentLevel(next.level);
  }
}

function beginOperation(): () => void {
  setPendingOperations((count) => count + 1);
  let finished = false;
  return () => {
    if (finished) {
      return;
    }
    finished = true;
    setPendingOperations((count) => Math.max(0, count - 1));
  };
}

function showToast(message: string, kind: ToastKind = "info") {
  setToast({ id: nextToastID++, kind, message });
}

export const appStore = {
  authStatus,
  user,
  profile,
  authLoading,
  authError,
  returnPath,
  activeLanguage,
  currentLevel,
  isBusy: createMemo(() => pendingOperations() > 0),
  toast,
  setAuthenticated,
  clearAuthentication,
  setLocalMode,
  setAuthLoading,
  setAuthError,
  setProfile,
  setActiveLanguage,
  setCurrentLevel,
  beginOperation,
  showToast,
  clearToast: () => setToast(null),
  rememberReturnPath: (path: string) => {
    if (path !== "/login") {
      setReturnPath(path);
    }
  },
  consumeReturnPath: () => {
    const path = returnPath();
    setReturnPath("/");
    return path;
  },
};

configureAuthCallbacks({
  onRefresh: appStore.setAuthenticated,
  onAuthenticationLost: appStore.clearAuthentication,
});
