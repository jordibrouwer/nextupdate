import { h } from './dom.js';

export function toast(message, kind = 'info') {
    const host = document.getElementById('toasts');
    const el = h('button', { class: `toast toast-${kind}`, type: 'button', dataset: { testid: 'toast' }, onclick: () => el.remove() }, message);
    host.append(el);
    setTimeout(() => el.remove(), kind === 'error' || kind === 'warn' ? 9000 : 5000);
}

export function badge(text, kind = 'neutral') {
    return h('span', { class: `badge badge-${kind}` }, text);
}

export function spinner() {
    return h('span', { class: 'spinner', role: 'img', 'aria-label': 'Working' });
}

export function confirmDialog({ title, body, confirmLabel = 'Confirm', danger = false }) {
    return new Promise((resolve) => {
        const dlg = h('dialog', { class: 'dialog', dataset: { testid: 'confirm-dialog' } },
            h('h2', {}, title),
            h('div', { class: 'dialog-body' }, body),
            h('div', { class: 'dialog-actions' },
                h('button', { class: 'btn', type: 'button', onclick: () => dlg.close('cancel') }, 'Cancel'),
                h('button', { class: `btn ${danger ? 'btn-danger' : 'btn-primary'}`, type: 'button', dataset: { testid: 'confirm-yes' }, onclick: () => dlg.close('yes') }, confirmLabel)));
        dlg.addEventListener('close', () => {
            resolve(dlg.returnValue === 'yes');
            dlg.remove();
        });
        document.body.append(dlg);
        dlg.showModal();
    });
}
