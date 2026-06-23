import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js";
import { APIError, listSkills, type APISchema } from "../api";
import { appStore } from "../store";

type SkillTree = APISchema<"SkillTree">;
type Skill = APISchema<"SkillProgress">;

export function SkillsView() {
  const [tree, setTree] = createSignal<SkillTree | null>(null);
  const [loading, setLoading] = createSignal(true);
  const [error, setError] = createSignal("");
  const [expandedSkill, setExpandedSkill] = createSignal("");

  const skillCount = createMemo(() => tree()?.categories.reduce((sum, category) => sum + category.skills.length, 0) ?? 0);
  const activeCount = createMemo(() => tree()?.categories.reduce((sum, category) => (
    sum + category.skills.filter((skill) => skill.tier > 0).length
  ), 0) ?? 0);
  const pendingCount = createMemo(() => tree()?.categories.reduce((sum, category) => (
    sum + category.skills.filter((skill) => skill.pending_verification).length
  ), 0) ?? 0);
  const promotionCount = createMemo(() => tree()?.categories.reduce((sum, category) => (
    sum + category.skills.filter((skill) => skill.recently_promoted).length
  ), 0) ?? 0);

  onMount(() => {
    void load();
  });

  const load = async () => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setTree(await listSkills(activeLanguageQuery()));
    } catch (err) {
      setError(skillsErrorMessage(err));
    } finally {
      finish();
      setLoading(false);
    }
  };

  const toggleExpanded = (skillID: string) => {
    setExpandedSkill((current) => current === skillID ? "" : skillID);
  };

  return (
    <section class="skills-view">
      <header class="view-heading skills-heading">
        <div>
          <h1>Skills</h1>
          <p>{tree() ? `${tree()!.language.toUpperCase()} competency map` : "Competency map"}</p>
        </div>
        <button class="secondary-button" type="button" disabled={loading()} onClick={() => void load()}>
          {loading() ? "Loading..." : "Refresh"}
        </button>
      </header>

      <Switch>
        <Match when={loading()}>
          <div class="skills-state" aria-busy="true">Loading skills...</div>
        </Match>
        <Match when={error()}>
          <div class="skills-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>
              Retry
            </button>
          </div>
        </Match>
        <Match when={tree() && skillCount() === 0}>
          <div class="skills-state empty-state">
            <h2>No skills defined</h2>
            <p>{tree()!.language.toUpperCase()} does not have a skill catalogue yet.</p>
          </div>
        </Match>
        <Match when={tree()}>
          <div class="skills-content">
            <div class="skills-metrics" aria-label="Skill summary">
              <Metric label="Skills" value={skillCount()} />
              <Metric label="Started" value={activeCount()} />
              <Metric label="Pending" value={pendingCount()} />
              <Metric label="Promoted" value={promotionCount()} />
            </div>

            <Show when={pendingCount() > 0 || promotionCount() > 0}>
              <div class="skill-notices" role="status">
                <Show when={pendingCount() > 0}>
                  <span>{pendingCount()} pending verification</span>
                </Show>
                <Show when={promotionCount() > 0}>
                  <span>{promotionCount()} recent promotion{promotionCount() === 1 ? "" : "s"}</span>
                </Show>
              </div>
            </Show>

            <div class="skill-category-list">
              <For each={tree()!.categories}>
                {(category) => (
                  <section class="skill-category" aria-labelledby={`skill-category-${category.id}`}>
                    <header class="skill-category-heading">
                      <div>
                        <h2 id={`skill-category-${category.id}`}>{category.title}</h2>
                        <p>{categoryProgressLabel(category.skills)}</p>
                      </div>
                      <span>{category.skills.length}</span>
                    </header>

                    <div class="skill-list">
                      <For each={category.skills}>
                        {(skill) => (
                          <SkillRow
                            skill={skill}
                            expanded={expandedSkill() === skill.skill_id}
                            onToggle={() => toggleExpanded(skill.skill_id)}
                          />
                        )}
                      </For>
                    </div>
                  </section>
                )}
              </For>
            </div>
          </div>
        </Match>
      </Switch>
    </section>
  );
}

function Metric(props: { label: string; value: number | string }) {
  return (
    <div class="skill-metric">
      <span>{props.label}</span>
      <strong>{props.value}</strong>
    </div>
  );
}

function SkillRow(props: { skill: Skill; expanded: boolean; onToggle: () => void }) {
  const progressPercent = () => `${Math.round(clampRatio(props.skill.progress_ratio) * 100)}%`;

  return (
    <article class="skill-row" data-tier={props.skill.tier} data-pending={props.skill.pending_verification || undefined}>
      <div class="skill-row-main">
        <div class="skill-title-line">
          <h3>{props.skill.name}</h3>
          <div class="skill-badges">
            <span class="tier-chip">{props.skill.tier_label}</span>
            <Show when={props.skill.pending_verification}>
              <span class="skill-state-chip" data-state="pending">Pending</span>
            </Show>
            <Show when={props.skill.recently_promoted}>
              <span class="skill-state-chip" data-state="promoted">Promoted</span>
            </Show>
          </div>
        </div>

        <div class="skill-progress-line">
          <div
            class="skill-progress"
            role="progressbar"
            aria-valuemin="0"
            aria-valuemax="100"
            aria-valuenow={Math.round(clampRatio(props.skill.progress_ratio) * 100)}
            aria-label={`${props.skill.name} tier progress`}
          >
            <span style={{ width: progressPercent() }} />
          </div>
          <span>{progressSummary(props.skill)}</span>
        </div>

        <Show when={props.expanded}>
          <dl class="skill-detail">
            <div>
              <dt>Description</dt>
              <dd>{props.skill.description || "No description provided."}</dd>
            </div>
            <div>
              <dt>Tier</dt>
              <dd>{props.skill.tier}/{props.skill.tier_count}</dd>
            </div>
            <div>
              <dt>XP</dt>
              <dd>{props.skill.xp}</dd>
            </div>
            <Show when={props.skill.last_verified_at}>
              {(lastVerifiedAt) => (
                <div>
                  <dt>Verified</dt>
                  <dd>{formatUnixSeconds(lastVerifiedAt())}</dd>
                </div>
              )}
            </Show>
          </dl>
        </Show>
      </div>

      <button
        class="secondary-button skill-detail-button"
        type="button"
        aria-expanded={props.expanded}
        onClick={props.onToggle}
      >
        {props.expanded ? "Close" : "Details"}
      </button>
    </article>
  );
}

function activeLanguageQuery(): { language?: string } {
  const language = appStore.activeLanguage();
  return language ? { language } : {};
}

function categoryProgressLabel(skills: Skill[]): string {
  const started = skills.filter((skill) => skill.tier > 0).length;
  const acquired = skills.filter((skill) => skill.tier >= skill.tier_count).length;
  if (acquired > 0) {
    return `${started}/${skills.length} started, ${acquired} acquired`;
  }
  return `${started}/${skills.length} started`;
}

function progressSummary(skill: Skill): string {
  if (skill.tier >= skill.tier_count) {
    return `${skill.xp} XP`;
  }
  if (skill.pending_verification) {
    return `${skill.xp} XP, verification pending`;
  }
  return `${skill.xp_to_next} XP to next`;
}

function clampRatio(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(1, Math.max(0, value));
}

function formatUnixSeconds(value: number): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value * 1000));
}

function skillsErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 400) {
    return "This language does not have an enabled skill catalogue.";
  }
  return "Skills could not be loaded.";
}
