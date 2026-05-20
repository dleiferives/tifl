import { api } from '../api.js';
import { h, mount, toast, loading } from '../ui.js';

export async function render(root, params) {
    const id = params.id;
    mount(root, loading('Loading session…'));

    try {
        const [session, story, tasks, calls] = await Promise.all([
            api.getSession(id),
            api.getStory(id).catch(() => null),
            api.getTasks(id).catch(() => []),
            api.getSessionLlmCalls(id).catch(() => []),
        ]);
        mount(root, h('div', {},
            renderHeader(session),
            story ? renderStory(story) : h('p', { class: 'muted' }, 'No story attached.'),
            renderGlossary(session.glossary),
            renderPlan(session.session_plan),
            renderTasks(tasks, id),
            renderCalls(calls),
        ));
    } catch (e) {
        toast(e.message, true);
        mount(root, h('div', { class: 'card' }, h('h2', {}, 'Error'), h('pre', {}, e.message)));
    }
}

function renderHeader(s) {
    return h('div', { class: 'card' },
        h('h2', {}, `Session ${s.session_id}`),
        h('p', { class: 'muted' }, new Date((s.generated_at || 0) * 1000).toLocaleString()),
        h('div', {},
            (s.constructions_targeted || []).map(c => h('span', { class: 'tag accent' }, c)),
        ),
    );
}

function renderStory(st) {
    return h('div', { class: 'card' },
        h('h2', {}, st.topic ? `Story — ${st.topic}` : 'Story'),
        h('div', { class: 'story-text' }, st.text),
        h('p', { class: 'muted' },
            `coverage: ${(st.estimated_coverage ?? 0).toFixed(3)} · new chunks: ${(st.new_chunks || []).length}`),
    );
}

function renderGlossary(glossary) {
    const entries = (glossary && glossary.entries) || [];
    if (!entries.length) return null;
    return h('div', { class: 'card' },
        h('h2', {}, 'Glossary'),
        h('table', {},
            h('thead', {}, h('tr', {}, h('th', {}, 'Greek'), h('th', {}, 'Context'), h('th', {}, 'Example'))),
            h('tbody', {}, ...entries.map(e => h('tr', {},
                h('td', {}, e.greek_text),
                h('td', { class: 'muted' }, e.context_greek || ''),
                h('td', {}, e.example_sentence_greek || ''),
            ))),
        ),
    );
}

function renderPlan(plan) {
    if (!plan || !Object.keys(plan).length) return null;
    return h('details', { class: 'card' },
        h('summary', {}, h('strong', {}, 'Session plan (LLM)')),
        h('pre', {}, JSON.stringify(plan, null, 2)),
    );
}

function renderTasks(tasks, sessionId) {
    if (!tasks.length) {
        return h('div', { class: 'card' }, h('h2', {}, 'Tasks'), h('p', { class: 'muted' }, 'No tasks.'));
    }
    return h('div', { class: 'card' },
        h('h2', {}, 'Tasks'),
        ...tasks.map(t => renderTask(t, sessionId)),
    );
}

function renderTask(task, sessionId) {
    const completed = !!task.completed_at;
    const block = h('div', { class: `task ${completed ? 'completed' : ''}` },
        h('h3', {}, `${task.task_type}${task.task_subtype ? ' · ' + task.task_subtype : ''}`),
        h('pre', {}, JSON.stringify(task.content || {}, null, 2)),
    );

    if (completed) {
        block.append(
            h('p', { class: 'muted' }, 'Submitted'),
            h('pre', {}, JSON.stringify(task.evaluation_result || { confidence: task.confidence_rating }, null, 2)),
        );
        return block;
    }

    const textarea = h('textarea', { placeholder: 'Type your response in Greek…' });
    let confidence = null;
    const confBtns = [1, 2, 3].map(n =>
        h('button', { type: 'button', onclick: (e) => {
            confidence = n;
            for (const b of e.currentTarget.parentElement.children) b.classList.remove('active');
            e.currentTarget.classList.add('active');
        }}, n === 1 ? '1 · fine' : n === 2 ? '2 · uncertain' : '3 · lost'),
    );

    const submitBtn = h('button', {
        type: 'button',
        onclick: async () => {
            submitBtn.disabled = true; submitBtn.textContent = 'Evaluating…';
            try {
                const res = await api.submitTask(task.task_id, {
                    learner_response: textarea.value || null,
                    confidence,
                });
                // re-render this view to pick up server state
                const r = await api.getTasks(sessionId);
                const updated = r.find(x => x.task_id === task.task_id);
                if (updated) {
                    block.replaceWith(renderTask(updated, sessionId));
                } else {
                    submitBtn.textContent = 'Submitted';
                }
            } catch (e) {
                toast(e.message, true);
                submitBtn.disabled = false; submitBtn.textContent = 'Submit';
            }
        },
    }, 'Submit');

    block.append(
        textarea,
        h('div', {}, h('label', {}, 'Confidence after this task')),
        h('div', { class: 'confidence' }, ...confBtns),
        h('div', { style: { marginTop: '0.6rem' } }, submitBtn),
    );
    return block;
}

function renderCalls(calls) {
    if (!calls.length) return null;
    return h('details', { class: 'card' },
        h('summary', {}, h('strong', {}, `AI calls for this session (${calls.length})`)),
        h('table', {},
            h('thead', {}, h('tr', {}, h('th', {}, 'kind'), h('th', {}, 'when'), h('th', {}, 'log'))),
            h('tbody', {}, ...calls.map(c => h('tr', {},
                h('td', {}, c.kind),
                h('td', {}, new Date((c.created_at || 0) * 1000).toLocaleTimeString()),
                h('td', {}, h('a', { href: `#/logs/${encodeURIComponent(c.log_file)}` }, c.log_file)),
            ))),
        ),
    );
}
