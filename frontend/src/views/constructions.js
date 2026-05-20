import { api } from '../api.js';
import { h, mount, loading, toast } from '../ui.js';

export async function render(root) {
    mount(root, loading());
    try {
        const rows = await api.listConstructions();
        rows.sort((a, b) => (b.gap_score || 0) - (a.gap_score || 0));
        mount(root, h('div', { class: 'card' },
            h('h2', {}, `Constructions (${rows.length})`),
            h('p', { class: 'muted' }, 'Higher gap_score = seen often in input, produced rarely. These get prioritised.'),
            h('table', {},
                h('thead', {}, h('tr', {},
                    h('th', {}, 'ID'), h('th', {}, 'Type'),
                    h('th', {}, 'Exposure'), h('th', {}, 'Prod. ✓'), h('th', {}, 'Prod. ✗'),
                    h('th', {}, 'Gap'), h('th', {}, 'Last targeted'),
                )),
                h('tbody', {}, ...rows.map(r => h('tr', {},
                    h('td', {}, r.construction_id),
                    h('td', { class: 'muted' }, r.construction_type),
                    h('td', {}, String(r.exposure_count)),
                    h('td', {}, String(r.production_correct)),
                    h('td', {}, String(r.production_errors)),
                    h('td', {}, (r.gap_score || 0).toFixed(2)),
                    h('td', {}, r.last_targeted ? new Date(r.last_targeted * 1000).toLocaleString() : '—'),
                ))),
            ),
        ));
    } catch (e) {
        toast(e.message, true);
    }
}
