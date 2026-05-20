import { api } from '../api.js';
import { h, mount, loading, toast } from '../ui.js';

export async function render(root) {
    mount(root, loading());
    try {
        const rows = await api.listChunks(false);
        mount(root, h('div', { class: 'card' },
            h('h2', {}, `Chunks (${rows.length})`),
            h('p', { class: 'muted' }, 'Vocabulary the learner has been exposed to.'),
            h('table', {},
                h('thead', {}, h('tr', {},
                    h('th', {}, 'Greek'), h('th', {}, 'Context'),
                    h('th', {}, 'Exposures'), h('th', {}, 'Produced'),
                    h('th', {}, 'Confidence'), h('th', {}, 'Available'),
                )),
                h('tbody', {}, ...rows.map(r => h('tr', {},
                    h('td', {}, r.greek_text),
                    h('td', { class: 'muted' }, r.context_greek || ''),
                    h('td', {}, String(r.exposure_count)),
                    h('td', {}, String(r.production_count)),
                    h('td', {}, (r.confidence_ratings || []).join(', ')),
                    h('td', {}, r.is_available ? '✓' : ''),
                ))),
            ),
        ));
    } catch (e) {
        toast(e.message, true);
    }
}
