export class ApiError extends Error {
    constructor(status, message) {
        super(message);
        this.status = status;
    }
}

const PUBLIC = ['/api/status', '/api/login', '/api/setup'];

async function request(method, path, body) {
    const opts = { method, headers: {}, credentials: 'same-origin' };
    if (method !== 'GET') opts.headers['X-NextUpdate'] = '1';
    if (body !== undefined) {
        opts.headers['Content-Type'] = 'application/json';
        opts.body = JSON.stringify(body);
    }
    let res;
    try {
        res = await fetch(path, opts);
    } catch {
        throw new ApiError(0, "Can't reach the server. Check your connection and try again.");
    }
    let data = null;
    const text = await res.text();
    if (text) {
        try { data = JSON.parse(text); } catch { /* not JSON */ }
    }
    if (!res.ok) {
        if (res.status === 401 && !PUBLIC.includes(path)) document.dispatchEvent(new CustomEvent('nu:signed-out'));
        throw new ApiError(res.status, (data && data.error) || `The request failed (${res.status}).`);
    }
    return data;
}

export const api = {
    get: (p) => request('GET', p),
    post: (p, b) => request('POST', p, b ?? {}),
    put: (p, b) => request('PUT', p, b),
    del: (p) => request('DELETE', p),
};
