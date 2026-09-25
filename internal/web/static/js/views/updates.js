import { h, clear } from '../dom.js';
import { icon } from '../icons.js';
import { api } from '../api.js';
import { toast, badge, spinner, confirmDialog } from '../ui.js';
import { onJobs, refreshJobs } from '../jobs.js';
import { renderMarkdown } from '../markdown.js';
import { versionChange, relativeTime, outcomeLabel } from '../format.js';

const enc = encodeURIComponent;
const wide = () => window.matchMedia('(min-width: 761px)').matches;
const CHANGE_LABEL = { major: 'Major', minor: 'Minor', patch: 'Patch' };

export function mountUpdates(container, params) {
    let containers = [];
    let updates = new Map();
    let history = [];
    let jobs = { running: [], failed: [] };
    let selected = params.name || '';
    let alive = true;
    const tracking = new Map(); // name -> { kind, topId } for jobs this view started
    const shownFailures = new Set();

    const list = h('nav', { class: 'list', 'aria-label': 'Containers' });
    const legend = h('div', { class: 'legend faint' },
        h('span', {}, h('kbd', {}, 'j'), ' ', h('kbd', {}, 'k'), ' move'),
        h('span', {}, h('kbd', {}, 'u'), ' update'),
        h('span', {}, h('kbd', {}, 'c'), ' check'));
    const detail = h('section', { class: 'detail-pane', dataset: { testid: 'detail' }, 'aria-live': 'polite' });
    const split = h('div', { class: 'split' }, h('div', { class: 'list-pane' }, list, legend), detail);
    clear(container).append(split);

    const busyNames = () => new Set(jobs.running.filter((j) => j.key !== 'check').map((j) => j.key));
    const isBusy = (name) => busyNames().has(name);
    const latestOk = (name) => history.find((e) => e.container === name && e.outcome === 'ok' && e.fromImage && e.fromImage !== e.toImage);

    async function load() {
        try {
            [containers, history] = await Promise.all([api.get('/api/containers'), api.get('/api/history?limit=100')]);
            const ups = await api.get('/api/updates');
            updates = new Map(ups.map((u) => [u.container, u]));
        } catch (e) {
            if (e.status !== 401) toast(e.message, 'error');
            return;
        }
        if (!alive) return;
        if (!selected && wide()) {
            const first = orderedNames()[0];
            if (first) {
                selected = first;
                location.replace(`#/updates/${enc(first)}`);
            }
        }
        render();
    }

    function orderedNames() {
        const groups = grouped();
        return [...groups.breaking, ...groups.updates, ...groups.current].map((c) => c.name);
    }

    function grouped() {
        const g = { breaking: [], updates: [], current: [] };
        for (const c of containers) {
            const u = updates.get(c.name);
            if (u && u.breaking) g.breaking.push(c);
            else if (u) g.updates.push(c);
            else g.current.push(c);
        }
        const byName = (a, b) => a.name.localeCompare(b.name);
        for (const k of Object.keys(g)) g[k].sort(byName);
        return g;
    }

    function render() {
        split.classList.toggle('detail-open', Boolean(selected));
        renderList();
        renderDetail();
    }

    function renderList() {
        const g = grouped();
        clear(list);
        if (!containers.length) {
            list.append(h('p', { class: 'empty' }, 'No containers found. Check that nextupdate can reach the Docker socket.'));
            return;
        }
        const section = (key, title, items) => items.length ? h('section', { dataset: { testid: `group-${key}` } },
            h('h2', { class: 'group-title' }, title, h('span', { class: 'count' }, String(items.length))),
            items.map(rowFor)) : null;
        list.append(
            section('breaking', 'Breaking', g.breaking),
            section('updates', 'Updates available', g.updates),
            section('current', 'Up to date', g.current));
    }

    function rowFor(c) {
        const u = updates.get(c.name);
        const change = u ? versionChange(u.oldVersion, u.newVersion) : '';
        return h('a', { class: 'row', href: `#/updates/${enc(c.name)}`, dataset: { name: c.name }, 'aria-current': selected === c.name ? 'true' : null },
            h('span', { class: 'row-main' },
                h('span', { class: 'row-name' }, c.name),
                h('span', { class: 'row-sub faint' }, u && (u.oldVersion || u.newVersion) ? `${u.oldVersion || 'unknown'} to ${u.newVersion || 'unknown'}` : c.image)),
            h('span', { class: 'row-side' },
                isBusy(c.name) ? spinner() : null,
                u && u.breaking ? badge('Breaking', 'danger') : (CHANGE_LABEL[change] ? badge(CHANGE_LABEL[change], 'accent') : (u ? badge('Update', 'accent') : null))));
    }

    function renderDetail() {
        clear(detail);
        const c = containers.find((x) => x.name === selected);
        if (!c) {
            detail.append(h('div', { class: 'empty-detail' }, selected ? `There is no container called ${selected}.` : 'Select a container to see its details.'));
            return;
        }
        const u = updates.get(c.name);
        const busy = isBusy(c.name);

        const policy = h('select', {
            id: 'policy', dataset: { testid: 'policy-select' }, 'aria-label': 'Update policy',
            onchange: () => saveSettings(c, { policy: policy.value }, `Policy for ${c.name} is now ${policy.value}`),
        },
            h('option', { value: 'notify' }, 'Notify only'),
            h('option', { value: 'auto' }, 'Update automatically'),
            h('option', { value: 'never' }, 'Never'));
        policy.value = c.policy;

        const back = h('a', { class: 'back', href: '#/updates', dataset: { testid: 'back' } }, icon('back'), 'Containers');

        detail.append(
            back,
            h('header', { class: 'detail-head' },
                h('div', {},
                    h('h1', {}, c.name),
                    h('p', { class: 'muted image-line' }, c.image, ' ', badge(c.source === 'compose' ? 'Compose' : 'Docker run'))),
                h('div', { class: 'policy' }, h('label', { for: 'policy' }, 'Policy'), policy)),
            u ? updateSection(c, u, busy) : currentSection(c, busy),
            settingsSection(c));
        if (u) loadChangelog(c);
    }

    function updateButton(c, u, busy) {
        return h('button', {
            class: 'btn btn-primary', type: 'button', dataset: { testid: 'update-button' }, 'aria-busy': String(busy),
            onclick: () => startUpdate(c, u),
        }, busy ? spinner() : null, busy ? 'Updating' : 'Update');
    }

    function updateSection(c, u, busy) {
        const change = versionChange(u.oldVersion, u.newVersion);
        return h('div', {},
            h('div', { class: 'action-bar' },
                h('div', {},
                    h('p', { class: 'version-line' }, `${u.oldVersion || 'unknown'} to ${u.newVersion || 'unknown'}`, ' ', CHANGE_LABEL[change] ? badge(CHANGE_LABEL[change], 'accent') : null),
                    h('p', { class: 'faint' }, u.detectedAt ? `Detected ${relativeTime(u.detectedAt)}` : '')),
                updateButton(c, u, busy)),
            u.breaking ? h('div', { class: 'box box-danger', dataset: { testid: 'breaking-box' } },
                h('h3', {}, icon('alert'), ' Breaking update'),
                h('ul', {}, u.reasons.map((r) => h('li', {}, r))),
                h('p', { class: 'hint' }, 'A rollback restores the container, not its data. Back up before you update.')) : null,
            h('section', { class: 'changelog', dataset: { testid: 'changelog' } }, h('h2', {}, 'Release notes'), h('div', { class: 'changelog-body' }, h('p', { class: 'faint' }, 'Loading release notes…'))));
    }

    function currentSection(c, busy) {
        const last = history.find((e) => e.container === c.name);
        const canRollback = Boolean(latestOk(c.name));
        return h('div', {},
            h('div', { class: 'action-bar' },
                h('div', {},
                    h('p', { class: 'version-line' }, icon('check'), ' Up to date'),
                    last ? h('p', { class: 'faint' }, `Last change: ${outcomeLabel(last.outcome).toLowerCase()} ${relativeTime(last.finishedAt)}`) : h('p', { class: 'faint' }, 'No updates recorded yet.')),
                canRollback ? h('button', {
                    class: 'btn', type: 'button', dataset: { testid: 'rollback-button' }, 'aria-busy': String(busy),
                    onclick: () => startRollback(c),
                }, busy ? spinner() : null, busy ? 'Rolling back' : 'Roll back') : null));
    }

    async function loadChangelog(c) {
        const body = detail.querySelector('.changelog-body');
        try {
            const data = await api.get(`/api/updates/${enc(c.name)}/changelog`);
            if (!alive || selected !== c.name || !body.isConnected) return;
            clear(body);
            if (!data.releases.length) {
                body.append(h('p', { class: 'muted' }, 'No release notes found.'),
                    data.repo ? h('p', {}, h('a', { href: `https://github.com/${data.repo}/releases`, target: '_blank', rel: 'noopener noreferrer' }, 'Open releases on GitHub ', icon('external', 14))) : null);
                return;
            }
            for (const r of data.releases) {
                body.append(h('article', { class: 'release' },
                    h('header', {},
                        h('h3', {}, r.tag, r.name && r.name !== r.tag ? ` · ${r.name}` : ''),
                        h('span', { class: 'faint' }, relativeTime(r.publishedAt)),
                        r.url ? h('a', { href: r.url, target: '_blank', rel: 'noopener noreferrer' }, 'View on GitHub ', icon('external', 14)) : null),
                    h('div', { class: 'md' }, renderMarkdown(r.body))));
            }
        } catch (e) {
            if (body.isConnected) body.replaceChildren(h('p', { class: 'error-text' }, e.message));
        }
    }

    function settingsSection(c) {
        const url = h('input', { id: 's-url', type: 'url', value: c.httpUrl || '', placeholder: 'http://container:8080/health' });
        const repo = h('input', { id: 's-repo', type: 'text', value: c.repo || '', placeholder: 'owner/name' });
        const win = h('input', { id: 's-window', type: 'number', min: '0', max: '3600', value: String(c.verifyWindowSeconds || 0) });
        return h('details', { class: 'card settings' },
            h('summary', {}, 'Container settings'),
            h('div', { class: 'field' }, h('label', { for: 's-url' }, 'Health check URL'), url, h('p', { class: 'hint' }, 'After an update this must answer with a 2xx status. Leave empty to rely on the Docker health check.')),
            h('div', { class: 'field' }, h('label', { for: 's-repo' }, 'Repository'), repo, h('p', { class: 'hint' }, 'The GitHub repository the release notes come from, as owner/name. Leave empty to detect it.')),
            h('div', { class: 'field' }, h('label', { for: 's-window' }, 'Verify window'), win, h('p', { class: 'hint' }, 'Seconds a new container gets to become healthy. 0 uses the default.')),
            h('button', {
                class: 'btn', type: 'button', dataset: { testid: 'settings-save' },
                onclick: () => saveSettings(c, { httpUrl: url.value, repo: repo.value, verifyWindowSeconds: Number(win.value || 0) }, `Settings for ${c.name} saved`),
            }, 'Save'));
    }

    async function saveSettings(c, patch, message) {
        const body = { policy: c.policy, httpUrl: c.httpUrl || '', repo: c.repo || '', verifyWindowSeconds: c.verifyWindowSeconds || 0, ...patch };
        try {
            await api.put(`/api/containers/${enc(c.name)}/settings`, body);
            Object.assign(c, { policy: body.policy, httpUrl: body.httpUrl, repo: body.repo, verifyWindowSeconds: body.verifyWindowSeconds });
            toast(message, 'success');
        } catch (e) {
            toast(e.message, 'error');
            if ('policy' in patch) renderDetail(); // put the select back to the saved policy
        }
    }

    async function startUpdate(c, u) {
        if (isBusy(c.name)) return;
        if (u.breaking) {
            const ok = await confirmDialog({
                title: `Update ${c.name}?`,
                body: h('div', {}, h('p', {}, 'This update looks breaking:'), h('ul', {}, u.reasons.map((r) => h('li', {}, r))), h('p', {}, 'A rollback restores the container, not its data.')),
                confirmLabel: 'Update anyway', danger: true,
            });
            if (!ok) return;
        }
        await startJob(c.name, 'update', `/api/updates/${enc(c.name)}/apply`);
    }

    function startRollback(c) {
        return startJob(c.name, 'rollback', `/api/containers/${enc(c.name)}/rollback`);
    }

    async function startJob(name, kind, path) {
        try {
            await api.post(path);
        } catch (e) {
            toast(e.message, e.status === 409 ? 'info' : 'error');
            return;
        }
        tracking.set(name, { kind, topId: history.length ? history[0].id : 0 });
        await refreshJobs();
    }

    function summarize(name, kind) {
        const t = tracking.get(name);
        const entry = history.find((e) => e.container === name && e.id > t.topId);
        if (!entry) return;
        if (kind === 'rollback') {
            if (entry.outcome === 'ok') toast(`Rolled back ${name}`, 'success');
            else toast(`Rollback of ${name} ${entry.outcome === 'rolled_back' ? 'was undone' : 'failed'}: ${entry.reason}`, 'error');
        } else if (entry.outcome === 'ok') toast(`Updated ${name}`, 'success');
        else if (entry.outcome === 'rolled_back') toast(`Update of ${name} was rolled back: ${entry.reason}`, 'warn');
        else toast(`Update of ${name} failed: ${entry.reason}`, 'error');
    }

    const stop = onJobs(async (now, prev) => {
        jobs = now;
        const running = new Set(now.running.map((j) => j.key));
        const finished = [...tracking.keys()].filter((n) => !running.has(n));
        const anyFinished = prev.running.some((j) => !running.has(j.key));
        const failedNow = new Set();
        for (const f of now.failed || []) {
            const id = `${f.key}|${f.at}`;
            if (!shownFailures.has(id)) {
                shownFailures.add(id);
                if (tracking.has(f.key)) {
                    failedNow.add(f.key);
                    toast(`${f.kind === 'rollback' ? 'Rollback' : 'Update'} of ${f.key} failed: ${f.error}`, 'error');
                }
            }
        }
        if (anyFinished || finished.length) {
            await load();
            for (const name of finished) {
                if (!failedNow.has(name)) summarize(name, tracking.get(name).kind);
                tracking.delete(name);
            }
        } else if (alive && containers.length) {
            render();
        }
    });

    function onKey(e) {
        if (e.metaKey || e.ctrlKey || e.altKey) return;
        if (document.querySelector('dialog[open]')) return;
        const t = e.target;
        if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
        const names = orderedNames();
        const i = names.indexOf(selected);
        const go = (n) => { if (n) location.hash = `#/updates/${enc(n)}`; };
        if (e.key === 'j' || e.key === 'ArrowDown') { e.preventDefault(); go(names[Math.min(i + 1, names.length - 1)]); }
        else if (e.key === 'k' || e.key === 'ArrowUp') { e.preventDefault(); go(names[Math.max(i - 1, 0)]); }
        else if (e.key === 'u') {
            const c = containers.find((x) => x.name === selected);
            const u = c && updates.get(c.name);
            if (c && u) startUpdate(c, u);
        } else if (e.key === 'c') {
            document.querySelector('[data-testid="check-button"]')?.click();
        }
    }
    document.addEventListener('keydown', onKey);

    load();
    return {
        select(name) {
            selected = name;
            render();
        },
        unmount() {
            alive = false;
            stop();
            document.removeEventListener('keydown', onKey);
        },
    };
}
