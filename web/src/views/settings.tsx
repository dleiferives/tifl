import { appStore } from "../store";

export function SettingsView() {
  return (
    <section>
      <h1>Settings</h1>
      <p>Theme and preference controls will be implemented in issue #63.</p>
      <dl class="status-list">
        <div>
          <dt>Active language</dt>
          <dd>{appStore.activeLanguage() || "Not loaded"}</dd>
        </div>
        <div>
          <dt>Current level</dt>
          <dd>{appStore.currentLevel()}</dd>
        </div>
      </dl>
    </section>
  );
}
