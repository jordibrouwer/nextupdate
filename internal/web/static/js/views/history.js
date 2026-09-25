import { h, clear, append } from '../dom.js';
import { api } from '../api.js';
import { badge } from '../ui.js';
import { relativeTime, shortImageId, outcomeLabel } from '../format.js';
import { onJobs } from '../jobs.js';

const KIND = { ok: 'success', rolled_back: 'warn', failed: 'danger' };

export function mountHistory(container) {
    let entries = [];
    let filter = '';
    let alive = true;
    const listEl = h('div', { class: 'history' });
    const select = h('select', { id: 'history-filter', onchange: () => { filter = select.value; render(); } });
    const page = h('div', { class: 'page' },
        h('div', { class: 'row-flex' },
            h('h1', {}, 'History'), h('span', { class: 'spacer' }),
            h('label', { for: 'history-filter', class: 'sr-only' }, 'Container'), select),
        listEl);
    clear(container).append(page);

    function render() {
        const names = [...new Set(entries.map((e) => e.container))].sort();
        append(clear(select), [h('option', { value: '' }, 'All containers'), names.map((n) => h('option', { value: n }, n))]);
        if (!names.includes(filter)) filter = '';
        select.value = filter;
        const shown = entries.filter((e) => !filter || e.container === filter);
        clear(listEl);
        if (!shown.length) {
            listEl.append(h('p', { class: 'muted' }, 'Nothing has been updated yet.'));
            return;
        }
        for (const e of shown) listEl.append(entry(e));
    }

    function entry(e) {
        return h('article', { class: 'card history-entry', dataset: { testid: 'history-entry', container: e.container } },
            h('header', { class: 'row-flex' },
                h('h2', {}, e.container),
                badge(outcomeLabel(e.outcome), KIND[e.outcome] || 'neutral'),
                h('span', { class: 'spacer' }),
                h('time', { class: 'faint', title: new Date(e.finishedAt).toLocaleString(), datetime: e.finishedAt }, relativeTime(e.finishedAt))),
            h('p', { class: 'muted image-line' }, e.image),
            e.fromImage || e.toImage ? h('p', { class: 'faint' }, h('code', {}, shortImageId(e.fromImage) || 'unknown'), ' to ', h('code', {}, shortImageId(e.toImage) || 'unknown')) : null,
            e.reason ? h('p', {}, e.reason) : null,
            e.log && e.log.length ? h('details', {}, h('summary', {}, 'Steps'), h('pre', { class: 'steps' }, e.log.join('\n'))) : null);
    }

    async function load() {
        try {
            entries = await api.get('/api/history?limit=100');
        } catch (err) {
            if (alive) listEl.replaceChildren(h('p', { class: 'error-text' }, err.message));
            return;
        }
        if (alive) render();
    }

    const stop = onJobs((now, prev) => {
        if (prev.running.length > now.running.length) load();
    });
    load();
    return { unmount() { alive = false; stop(); } };
}
