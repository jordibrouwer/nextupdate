// Pure helpers. No DOM access, so Node can test them.

export function parseVersion(s) {
    if (typeof s !== 'string') return null;
    let v = s.trim().replace(/^v/, '');
    v = v.split('+')[0];
    const dash = v.indexOf('-');
    const core = dash === -1 ? v : v.slice(0, dash);
    const pre = dash === -1 ? '' : v.slice(dash + 1);
    const parts = core.split('.');
    if (!core || parts.length > 3 || parts.some((p) => !/^\d+$/.test(p))) return null;
    const [major, minor = 0, patch = 0] = parts.map(Number);
    return { major, minor, patch, pre };
}

function compare(a, b) {
    for (const k of ['major', 'minor', 'patch']) {
        if (a[k] !== b[k]) return a[k] < b[k] ? -1 : 1;
    }
    if (a.pre === b.pre) return 0;
    if (a.pre === '') return 1;
    if (b.pre === '') return -1;
    return a.pre < b.pre ? -1 : 1;
}

export function versionChange(from, to) {
    const a = parseVersion(from);
    const b = parseVersion(to);
    if (!a || !b) return '';
    const c = compare(a, b);
    if (c > 0) return 'downgrade';
    if (c === 0) return 'same';
    if (b.major !== a.major) return 'major';
    if (b.minor !== a.minor) return a.major === 0 ? 'major' : 'minor';
    return 'patch';
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export function relativeTime(iso, now = Date.now()) {
    const t = Date.parse(iso);
    if (!iso || Number.isNaN(t) || t > now) return '';
    const s = Math.floor((now - t) / 1000);
    if (s < 45) return 'just now';
    const m = Math.floor(s / 60);
    if (m < 60) return plural(m, 'minute', 'minutes') + ' ago';
    const h = Math.floor(m / 60);
    if (h < 24) return plural(h, 'hour', 'hours') + ' ago';
    const d = Math.floor(h / 24);
    if (d < 30) return plural(d, 'day', 'days') + ' ago';
    const date = new Date(t);
    return `${date.getUTCDate()} ${MONTHS[date.getUTCMonth()]} ${date.getUTCFullYear()}`;
}

export function shortImageId(id) {
    return id ? id.replace(/^sha256:/, '').slice(0, 12) : '';
}

export function plural(n, one, many) {
    return `${n} ${n === 1 ? one : many}`;
}

export function outcomeLabel(outcome) {
    return { ok: 'Updated', rolled_back: 'Rolled back', failed: 'Failed' }[outcome] || outcome;
}
