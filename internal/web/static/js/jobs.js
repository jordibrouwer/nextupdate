import { api } from './api.js';

const listeners = new Set();
let timer = null;
let last = { running: [], failed: [], lastCheck: '' };

function schedule() {
    clearTimeout(timer);
    if (!listeners.size) return;
    timer = setTimeout(() => refreshJobs().catch(() => schedule()), last.running.length ? 1500 : 15000);
}

export async function refreshJobs() {
    const prev = last;
    last = await api.get('/api/jobs');
    for (const fn of [...listeners]) fn(last, prev);
    schedule();
    return last;
}

/** fn(current, previous) runs on every poll. Returns an unsubscribe function. */
export function onJobs(fn) {
    listeners.add(fn);
    fn(last, last);
    if (listeners.size === 1) refreshJobs().catch(() => schedule());
    return () => {
        listeners.delete(fn);
        if (!listeners.size) clearTimeout(timer);
    };
}

export function currentJobs() {
    return last;
}
