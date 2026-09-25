import { h, clear } from './dom.js';
import { icon } from './icons.js';
import { api } from './api.js';
import { toast, spinner } from './ui.js';
import { onJobs, refreshJobs } from './jobs.js';
import { relativeTime } from './format.js';
import { renderAuth } from './views/login.js';
import { mountUpdates } from './views/updates.js';
import { mountHistory } from './views/history.js';
import { mountSettings } from './views/settings.js';

const root = document.getElementById('app');

const ROUTES = {
    updates: mountUpdates,
    history: mountHistory,
    settings: mountSettings,
};

let current = null; // { unmount, select? }
let unsubscribeJobs = null;

function parseHash() {
    const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
    const view = ROUTES[parts[0]] ? parts[0] : 'updates';
    const rest = ROUTES[parts[0]] ? parts.slice(1) : [];
    return { view, params: { name: rest.length ? decodeURIComponent(rest.join('/')) : '' } };
}

function themeState() {
    return document.documentElement.dataset.theme || 'auto';
}

function cycleTheme() {
    const next = { auto: 'light', light: 'dark', dark: 'auto' }[themeState()];
    if (next === 'auto') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = next;
    try {
        if (next === 'auto') localStorage.removeItem('nu-theme');
        else localStorage.setItem('nu-theme', next);
    } catch { /* storage blocked: the choice lasts until reload */ }
    return next;
}

function showShell() {
    const main = h('main', { class: 'main', id: 'view' });
    const navLinks = ['updates', 'history', 'settings'].map((v) => h('a', { href: `#/${v}`, dataset: { view: v } }, v[0].toUpperCase() + v.slice(1)));
    const lastCheck = h('span', { class: 'last-check', dataset: { testid: 'last-check' } });
    const checkBtn = h('button', {
        class: 'btn', type: 'button', dataset: { testid: 'check-button' }, 'aria-busy': 'false',
        onclick: async () => {
            try {
                await api.post('/api/check');
                await refreshJobs();
            } catch (e) {
                toast(e.message, e.status === 409 ? 'info' : 'error');
            }
        },
    }, icon('refresh'), h('span', {}, 'Check now'));
    const themeBtn = h('button', {
        class: 'btn btn-ghost', type: 'button', 'aria-label': `Theme: ${themeState()}. Switch theme`, title: `Theme: ${themeState()}`,
        onclick: () => {
            const next = cycleTheme();
            themeBtn.setAttribute('aria-label', `Theme: ${next}. Switch theme`);
            themeBtn.title = `Theme: ${next}`;
        },
    }, icon('contrast'));
    const signOut = h('button', {
        class: 'btn btn-ghost', type: 'button',
        onclick: async () => {
            try { await api.post('/api/logout'); } catch { /* the cookie is cleared either way */ }
            boot();
        },
    }, 'Sign out');

    clear(root).append(h('div', { class: 'shell' },
        h('header', { class: 'header' },
            h('a', { class: 'brand', href: '#/updates' }, icon('box', 20), 'nextupdate'),
            h('nav', { class: 'nav', 'aria-label': 'Main' }, navLinks),
            h('div', { class: 'header-end' }, lastCheck, checkBtn, themeBtn, signOut)),
        main));

    let tick = null;
    const paintLastCheck = (iso) => {
        const rel = relativeTime(iso);
        lastCheck.textContent = rel ? `Checked ${rel}` : 'Not checked yet';
    };
    unsubscribeJobs?.();
    unsubscribeJobs = onJobs((jobs) => {
        const checking = jobs.running.some((j) => j.key === 'check');
        checkBtn.setAttribute('aria-busy', String(checking));
        clear(checkBtn).append(checking ? spinner() : icon('refresh'), h('span', {}, checking ? 'Checking' : 'Check now'));
        paintLastCheck(jobs.lastCheck);
        clearInterval(tick);
        tick = setInterval(() => paintLastCheck(jobs.lastCheck), 30000);
    });

    // A view may offer select(name) so a change inside the view (for example
    // another container in the updates list) does not rebuild the whole page.
    let mountedView = '';
    const route = () => {
        const { view, params } = parseHash();
        for (const a of navLinks) {
            if (a.dataset.view === view) a.setAttribute('aria-current', 'page');
            else a.removeAttribute('aria-current');
        }
        refreshJobs().catch(() => { /* the poller retries; a 401 already signs out */ });
        if (view === mountedView && current && typeof current.select === 'function') {
            current.select(params.name);
            return;
        }
        current?.unmount();
        clear(main);
        mountedView = view;
        current = ROUTES[view](main, params);
    };
    window.onhashchange = route;
    if (!location.hash) history.replaceState(null, '', '#/updates');
    route();
}

export async function boot() {
    current?.unmount();
    current = null;
    unsubscribeJobs?.();
    unsubscribeJobs = null;
    window.onhashchange = null;
    let status;
    try {
        status = await api.get('/api/status');
    } catch (e) {
        clear(root).append(h('main', { class: 'auth' }, h('div', { class: 'card' }, h('h1', {}, "Can't reach nextupdate"), h('p', { class: 'muted' }, e.message))));
        return;
    }
    if (status.setupNeeded) return renderAuth(root, 'setup', boot);
    if (!status.signedIn) return renderAuth(root, 'login', boot);
    showShell();
}

document.addEventListener('nu:signed-out', () => boot());
boot();
