import { api } from '../api.js';
import { h, mount, loading, toast } from '../ui.js';

export async function render(root) {
    mount(root, loading());
    try {
        const rows = await api.listSessions();
        mount(root, h('div', { class: 'card' },
            h('h2', {}, `Sessions (${rows.length})`),
            h('table', {},
                h('thead', {}, h('tr', {},
                    h('th', {}, 'When'), h('th', {}, 'Guidance'), h('th', {}, 'Targets'), h('th', {}, ''),
                )),
                h('tbody', {}, ...rows.map(r => h('tr', {},
                    h('td', {}, new Date((r.generated_at || 0) * 1000).toLocaleString()),
                    h('td', {}, JSON.stringify(r.user_guidance || {})),
                    h('td', {}, (r.constructions_targeted || []).map(c => h('span', { class: 'tag accent' }, c))),
                    h('td', {}, h('a', { href: `#/session/${r.session_id}` }, 'open')),
                ))),
            ),
        ));
    } catch (e) {
        toast(e.message, true);
    }
}
