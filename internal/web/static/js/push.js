import { api } from './api.js';

export function bytesFromBase64Url(s) {
    const pad = '='.repeat((4 - (s.length % 4)) % 4);
    const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
    return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

/** 'unsupported' | 'insecure' | 'denied' | 'subscribed' | 'off' */
export async function pushState() {
    if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) return 'unsupported';
    if (!window.isSecureContext) return 'insecure';
    if (Notification.permission === 'denied') return 'denied';
    try {
        const reg = await navigator.serviceWorker.getRegistration('/');
        const sub = reg && (await reg.pushManager.getSubscription());
        return sub ? 'subscribed' : 'off';
    } catch {
        return 'off';
    }
}

export async function enablePush() {
    const reg = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    const permission = await Notification.requestPermission();
    if (permission !== 'granted') throw new Error('Notifications were not allowed, so push stays off.');
    const { publicKey } = await api.get('/api/push/key');
    let sub;
    try {
        sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytesFromBase64Url(publicKey) });
    } catch {
        throw new Error("This browser couldn't subscribe to push. It may not have a push service available.");
    }
    await api.post('/api/push/subscribe', sub.toJSON());
}

export async function disablePush() {
    const reg = await navigator.serviceWorker.getRegistration('/');
    const sub = reg && (await reg.pushManager.getSubscription());
    if (!sub) return;
    const endpoint = sub.endpoint;
    await sub.unsubscribe();
    await api.post('/api/push/unsubscribe', { endpoint });
}

export function sendTestPush() {
    return api.post('/api/push/test');
}
