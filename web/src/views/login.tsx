import { createSignal, Show } from "solid-js";
import { createAccount, signIn, signOut, signOutEverywhere } from "../auth";
import { appStore } from "../store";

export function LoginView() {
  return (
    <Show when={appStore.authStatus() === "authenticated"} fallback={<AuthenticationForms />}>
      <AccountPanel />
    </Show>
  );
}

function AuthenticationForms() {
  const [mode, setMode] = createSignal<"login" | "register">("login");
  const [email, setEmail] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [confirmPassword, setConfirmPassword] = createSignal("");

  const chooseMode = (next: "login" | "register") => {
    setMode(next);
    setPassword("");
    setConfirmPassword("");
    appStore.setAuthError("");
  };

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    if (mode() === "register" && password() !== confirmPassword()) {
      appStore.setAuthError("Passwords do not match.");
      return;
    }
    const credentials = { email: email(), password: password() };
    if (mode() === "login") {
      await signIn(credentials);
    } else {
      await createAccount(credentials);
    }
  };

  return (
    <section class="auth-card">
      <p class="auth-product">tifl</p>
      <h1>{mode() === "login" ? "Log in" : "Create account"}</h1>
      <div class="auth-tabs" role="tablist" aria-label="Authentication">
        <button type="button" role="tab" aria-selected={mode() === "login"} onClick={() => chooseMode("login")}>
          Log in
        </button>
        <button type="button" role="tab" aria-selected={mode() === "register"} onClick={() => chooseMode("register")}>
          Register
        </button>
      </div>
      <form class="auth-form" onSubmit={submit}>
        <label>
          Email
          <input
            type="email"
            name="email"
            autocomplete="email"
            required
            value={email()}
            onInput={(event) => setEmail(event.currentTarget.value)}
          />
        </label>
        <label>
          Password
          <input
            type="password"
            name="password"
            autocomplete={mode() === "login" ? "current-password" : "new-password"}
            minlength={mode() === "register" ? 15 : undefined}
            maxlength="128"
            required
            value={password()}
            onInput={(event) => setPassword(event.currentTarget.value)}
          />
        </label>
        <Show when={mode() === "register"}>
          <label>
            Confirm password
            <input
              type="password"
              name="confirm-password"
              autocomplete="new-password"
              minlength="15"
              maxlength="128"
              required
              value={confirmPassword()}
              onInput={(event) => setConfirmPassword(event.currentTarget.value)}
            />
          </label>
          <p class="field-help">Use 15–128 characters. Spaces are allowed.</p>
        </Show>
        <Show when={appStore.authError()}>
          <p class="form-error" role="alert">{appStore.authError()}</p>
        </Show>
        <button class="primary-button" type="submit" disabled={appStore.authLoading()}>
          {appStore.authLoading()
            ? "Please wait…"
            : mode() === "login" ? "Log in" : "Create account"}
        </button>
      </form>
    </section>
  );
}

function AccountPanel() {
  const logoutEverywhere = async () => {
    if (window.confirm("Log out of tifl on every device?")) {
      await signOutEverywhere();
    }
  };

  return (
    <section class="account-card">
      <h1>Account</h1>
      <p>Signed in as <strong>{appStore.user()?.email}</strong></p>
      <div class="account-actions">
        <button class="primary-button" type="button" disabled={appStore.authLoading()} onClick={signOut}>
          Log out
        </button>
        <button class="danger-button" type="button" disabled={appStore.authLoading()} onClick={logoutEverywhere}>
          Log out everywhere
        </button>
      </div>
      <p class="field-help">“Log out everywhere” revokes refresh sessions on all devices.</p>
    </section>
  );
}
