import { api } from '../api.js';
import { h, mount, toast, loading } from '../ui.js';
import { navigate } from '../router.js';
import { loadPrefs, savePrefs } from '../prefs.js';

export async function render(root) {
    mount(root, loading('Loading configuration…'));
    let levels, taskTypes;
    try {
        [levels, taskTypes] = await Promise.all([api.listLevels(), api.listTaskTypes()]);
    } catch (e) {
        toast(e.message, true);
        mount(root, h('div', { class: 'card' }, h('h2', {}, 'Error loading config')));
        return;
    }

    const prefs = loadPrefs();
    const initialLevel = prefs.level || levels.default;

    const state = {
        level: initialLevel,
        taskTypes: new Set(prefs.taskTypes || levelDefaults(levels, initialLevel)),
        topic: prefs.topic || '',
        focus: prefs.focus || '',
        difficulty: prefs.difficulty || '',
    };

    const view = h('div', {});
    rerender();
    mount(root, view);

    // recent sessions panel — loaded async
    const recent = h('div', { class: 'card' }, h('h2', {}, 'Recent sessions'), loading());
    view.append(recent);
    api.listSessions().then(rows => {
        recent.replaceChildren(
            h('h2', {}, 'Recent sessions'),
            rows.length === 0
                ? h('p', { class: 'muted' }, 'No sessions yet.')
                : h('table', {},
                    h('thead', {}, h('tr', {},
                        h('th', {}, 'When'), h('th', {}, 'Level'), h('th', {}, 'Targets'), h('th', {}, ''),
                    )),
                    h('tbody', {}, ...rows.map(r => h('tr', {},
                        h('td', {}, new Date((r.generated_at || 0) * 1000).toLocaleString()),
                        h('td', {}, (r.user_guidance && r.user_guidance.level) || '—'),
                        h('td', {}, (r.constructions_targeted || []).map(c =>
                            h('span', { class: 'tag accent' }, c))),
                        h('td', {}, h('a', { href: `#/session/${r.session_id}` }, 'open')),
                    ))),
                ),
        );
    }).catch(e => recent.replaceChildren(h('p', { class: 'err' }, e.message)));

    function rerender() {
        // keep the recent panel; rebuild form
        const form = buildForm(state, levels, taskTypes, rerender);
        if (view.firstChild) view.replaceChild(form, view.firstChild);
        else view.append(form);
    }
}

function levelDefaults(levels, levelId) {
    const lvl = levels.levels.find(l => l.id === levelId);
    return lvl ? lvl.default_task_types : [];
}

function buildForm(state, levels, taskTypes, rerender) {
    const form = h('form', { class: 'card', onsubmit: (e) => onSubmit(e, state) });

    form.append(h('h2', {}, 'Generate a session'));
    form.append(h('p', { class: 'muted' },
        'Pick a level and what tasks to generate. Topic / focus are optional.'));

    // ---- Level picker ----
    form.append(h('h3', {}, 'Level'));
    const levelRow = h('div', { class: 'row' });
    for (const lvl of levels.levels) {
        const active = state.level === lvl.id;
        levelRow.append(h('div', {
            class: 'col level-card' + (active ? ' active' : ''),
            onclick: () => {
                state.level = lvl.id;
                state.taskTypes = new Set(lvl.default_task_types);
                rerender();
            },
        },
            h('div', { class: 'level-label' }, lvl.label),
            h('div', { class: 'muted level-desc' }, lvl.description),
            h('div', { class: 'muted level-meta' },
                `coverage ${Math.round(lvl.coverage_target * 100)}% · up to ${lvl.max_new_chunks} new chunks`),
        ));
    }
    form.append(levelRow);

    // ---- Task picker grouped by difficulty ----
    form.append(h('h3', {}, 'Tasks to generate'));
    const grouped = { easy: [], medium: [], hard: [] };
    for (const t of taskTypes) (grouped[t.difficulty] || grouped.easy).push(t);

    const taskWrap = h('div', { class: 'task-picker' });
    for (const diff of ['easy', 'medium', 'hard']) {
        if (!grouped[diff].length) continue;
        taskWrap.append(h('div', { class: 'task-group' },
            h('div', { class: 'task-group-title' }, diff.toUpperCase()),
            ...grouped[diff].map(t => {
                const checked = state.taskTypes.has(t.id);
                return h('label', { class: 'task-option' + (checked ? ' active' : '') },
                    h('input', {
                        type: 'checkbox',
                        checked: checked,
                        onchange: (e) => {
                            if (e.target.checked) state.taskTypes.add(t.id);
                            else state.taskTypes.delete(t.id);
                            rerender();
                        },
                    }),
                    h('div', {},
                        h('div', { class: 'task-label' }, t.label),
                        h('div', { class: 'muted task-desc' }, t.description),
                    ),
                );
            }),
        ));
    }
    form.append(taskWrap);
    form.append(h('p', { class: 'muted' },
        `${state.taskTypes.size} task${state.taskTypes.size === 1 ? '' : 's'} selected.`));

    // ---- Optional guidance ----
    form.append(h('h3', {}, 'Optional guidance'));
    form.append(h('div', { class: 'row' },
        h('div', { class: 'col' },
            h('label', { for: 'topic' }, 'Topic'),
            h('input', {
                type: 'text', id: 'topic', value: state.topic,
                placeholder: 'e.g. καφές, μουσική',
                oninput: (e) => { state.topic = e.target.value; },
            }),
        ),
        h('div', { class: 'col' },
            h('label', { for: 'focus' }, 'Grammar focus'),
            h('input', {
                type: 'text', id: 'focus', value: state.focus,
                placeholder: 'e.g. genitive, past tense',
                oninput: (e) => { state.focus = e.target.value; },
            }),
        ),
        h('div', { class: 'col' },
            h('label', { for: 'difficulty' }, 'Difficulty signal'),
            (() => {
                const sel = h('select', { id: 'difficulty',
                    onchange: (e) => { state.difficulty = e.target.value; }},
                    h('option', { value: '' }, 'auto'),
                    h('option', { value: 'easier' }, 'easier'),
                    h('option', { value: 'push_me' }, 'push me'),
                );
                sel.value = state.difficulty;
                return sel;
            })(),
        ),
    ));

    // ---- Submit ----
    const btn = h('button', { type: 'submit' }, 'Generate session');
    form.append(h('div', { style: { marginTop: '1rem' } }, btn));
    return form;
}

async function onSubmit(ev, state) {
    ev.preventDefault();
    if (state.taskTypes.size === 0) {
        toast('Pick at least one task type.', true);
        return;
    }
    const guidance = {};
    if (state.topic) guidance.topic = state.topic;
    if (state.focus) guidance.construction_focus = state.focus;
    if (state.difficulty) guidance.difficulty_signal = state.difficulty;

    const body = {
        guidance,
        level: state.level,
        task_types: Array.from(state.taskTypes),
    };
    savePrefs({
        level: state.level,
        taskTypes: Array.from(state.taskTypes),
        topic: state.topic, focus: state.focus, difficulty: state.difficulty,
    });

    const btn = ev.target.querySelector('button[type=submit]');
    btn.disabled = true; btn.textContent = 'Generating…';
    try {
        const res = await api.generateSession(body);
        navigate(`#/session/${res.session_id}`);
    } catch (e) {
        toast(e.message, true);
        btn.disabled = false; btn.textContent = 'Generate session';
    }
}
