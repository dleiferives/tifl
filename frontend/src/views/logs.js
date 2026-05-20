import { api } from '../api.js';
import { h, mount, toast, loading } from '../ui.js';

export async function renderList(root) {
    mount(root, loading());
    try {
        const rows = await api.listLogs();
        mount(root, h('div', { class: 'card' },
            h('h2', {}, `AI Logs (${rows.length})`),
            h('p', { class: 'muted' }, 'Every LLM call is persisted to disk under ai_logs/.'),
            h('table', {},
                h('thead', {}, h('tr', {},
                    h('th', {}, 'time'), h('th', {}, 'kind'), h('th', {}, 'duration'), h('th', {}, 'file'),
                )),
                h('tbody', {}, ...rows.map(r => h('tr', {},
                    h('td', {}, r.ts || ''),
                    h('td', {}, r.kind || ''),
                    h('td', {}, r.duration_s != null ? r.duration_s + 's' : ''),
                    h('td', {}, h('a', { href: `#/logs/${encodeURIComponent(r.file)}` }, r.file)),
                ))),
            ),
        ));
    } catch (e) {
        toast(e.message, true);
    }
}

export async function renderOne(root, params) {
    const file = decodeURIComponent(params.file);
    mount(root, loading());
    try {
        const data = await api.getLog(file);
        mount(root, h('div', { class: 'card' },
            h('h2', {}, file),
            h('p', { class: 'muted' },
                `kind: ${data.kind} · model: ${data.model} · ${data.duration_s}s`),
            h('details', { open: true }, h('summary', {}, 'Prompt'), h('pre', {}, data.prompt || '')),
            h('details', { open: true }, h('summary', {}, 'Result text'), h('pre', {}, data.result_text || '')),
            h('details', {}, h('summary', {}, 'Parsed JSON'), h('pre', {}, JSON.stringify(data.parsed_json || null, null, 2))),
            h('details', {}, h('summary', {}, 'Raw stdout'), h('pre', {}, data.raw_stdout || '')),
            data.error ? h('p', { class: 'err' }, data.error) : null,
        ));
    } catch (e) {
        toast(e.message, true);
    }
}
