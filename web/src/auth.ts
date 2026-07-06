import {
  APIError,
  getAccessToken,
  getProfile,
  login,
  logout,
  logoutAll,
  refresh,
  register,
  type APIRequest,
  type APIResponse,
} from "./api";
import { routeHref } from "./router";
import { appStore } from "./store";

type AuthResponse = APIResponse<"login", 200>;

export async function initializeAuthentication() {
  try {
    const profile = await getProfile();
    if (getAccessToken()) {
      appStore.setProfile(profile);
    } else {
      appStore.setLocalMode(profile);
    }
    void appStore.refreshAdminContext();
    return;
  } catch {
    // In JWT mode, the profile request may still fail before a refresh cookie is
    // accepted; fall through to an explicit refresh attempt.
  }
  try {
    await acceptAuthentication(await refresh());
    return;
  } catch (error) {
    if (error instanceof APIError && (error.status === 404 || error.status === 405)) {
      appStore.clearAuthentication();
      return;
    }
  }
  appStore.clearAuthentication();
}

export async function signIn(credentials: APIRequest<"login">) {
  return runAuthOperation(async () => {
    await acceptAuthentication(await login(credentials));
    navigateAfterAuthentication();
  }, "login");
}

export async function createAccount(credentials: APIRequest<"register">) {
  return runAuthOperation(async () => {
    await acceptAuthentication(await register(credentials));
    navigateAfterAuthentication();
  }, "register");
}

export async function signOut() {
  appStore.setAuthLoading(true);
  appStore.setAuthError("");
  try {
    await logout();
  } catch {
    appStore.showToast("The server could not confirm logout. Local credentials were cleared.", "error");
  } finally {
    appStore.clearAuthentication();
    window.location.hash = routeHref("/login");
  }
}

export async function signOutEverywhere() {
  appStore.setAuthLoading(true);
  appStore.setAuthError("");
  try {
    await logoutAll();
  } catch {
    appStore.showToast("The server could not confirm logout from every device.", "error");
  } finally {
    appStore.clearAuthentication();
    window.location.hash = routeHref("/login");
  }
}

async function acceptAuthentication(response: AuthResponse) {
  appStore.setAuthenticated(response);
  try {
    appStore.setProfile(await getProfile());
  } catch {
    appStore.setProfile(null);
  }
  void appStore.refreshAdminContext();
}

async function runAuthOperation(operation: () => Promise<void>, kind: "login" | "register") {
  appStore.setAuthLoading(true);
  appStore.setAuthError("");
  try {
    await operation();
  } catch (error) {
    appStore.setAuthError(authMessage(error, kind));
  } finally {
    appStore.setAuthLoading(false);
  }
}

function navigateAfterAuthentication() {
  window.location.hash = routeHref(appStore.consumeReturnPath());
}

function authMessage(error: unknown, kind: "login" | "register"): string {
  if (!(error instanceof APIError)) {
    return "Authentication is temporarily unavailable. Try again.";
  }
  if (error.status === 429) {
    return "Too many attempts. Wait a minute and try again.";
  }
  if (kind === "login" && error.status === 401) {
    return "Email or password is incorrect.";
  }
  if (kind === "register" && error.status === 409) {
    return "Unable to create an account with those details.";
  }
  if (error.status === 400) {
    return kind === "register"
      ? "Enter a valid email and a password between 15 and 128 characters."
      : "Check the email and password and try again.";
  }
  return "Authentication is temporarily unavailable. Try again.";
}
