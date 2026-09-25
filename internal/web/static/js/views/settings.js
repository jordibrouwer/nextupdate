import { h, clear, append } from '../dom.js';
import { api } from '../api.js';
import { toast, confirmDialog, badge } from '../ui.js';

const MASK = '********';

export function mountSettings(container) {
    let alive = true;
    let types = [];
    let notifiers = [];
    let me = { name: '' };
    let token = '';
    let tokenShown = false;

    const notifiersHost = h('div', {});
    const widgetHost = h('div', {});
    const accountHost = h('div', {});
    const extraHost = h('div', {}); // the push card is added here (Task 8)
    const page = h('div', { class: 'page' }, h('h1', {}, 'Settings'),
        h('section', { class: 'card' }, h('h2', {}, 'Notifications'), notifiersHost),
        extraHost,
        h('section', { class: 'card' }, h('h2', {}, 'nextdash widget'), widgetHost),
        h('section', { class: 'card' }, h('h2', {}, 'Account'), accountHost));
    clear(container).append(page);

    const typeLabel = (t) => (types.find((x) => x.type === t) || { label: t }).label;

    // ---- notifiers ----
    // editing: undefined = list only, null = new notifier form, object = edit that notifier
    function renderNotifiers(editing) {
        clear(notifiersHost);
        if (!notifiers.length && editing === undefined) notifiersHost.append(h('p', { class: 'muted' }, 'No notifiers yet. Add one to hear about updates outside this page.'));
        for (const n of notifiers) {
            if (editing && editing.id === n.id) continue;
            const enabled = h('input', { type: 'checkbox', checked: n.enabled, id: `en-${n.id}`, onchange: () => save({ ...n, enabled: enabled.checked }, false) });
            notifiersHost.append(h('div', { class: 'row-flex notifier-row', dataset: { testid: 'notifier-row' } },
                h('strong', {}, n.name), badge(typeLabel(n.type)), h('span', { class: 'spacer' }),
                h('label', { class: 'check', for: `en-${n.id}` }, enabled, 'Enabled'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => testSend(n) }, 'Test'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => renderNotifiers(n) }, 'Edit'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => remove(n) }, 'Delete')));
        }
        if (editing !== undefined) notifiersHost.append(form(editing));
        else notifiersHost.append(h('button', { class: 'btn', type: 'button', dataset: { testid: 'add-notifier' }, onclick: () => renderNotifiers(null) }, 'Add notifier'));
    }

    function form(existing) {
        const error = h('p', { class: 'error-text', dataset: { testid: 'form-error' }, hidden: true });
        const name = h('input', { id: 'n-name', type: 'text', value: existing ? existing.name : '' });
        const type = h('select', { id: 'n-type', onchange: () => paintFields() }, types.map((t) => h('option', { value: t.type }, t.label)));
        if (existing) { type.value = existing.type; type.disabled = true; }
        const enabled = h('input', { type: 'checkbox', id: 'n-enabled', checked: existing ? existing.enabled : true });
        const fieldsHost = h('div', {});
        let inputs = {};

        function paintFields() {
            clear(fieldsHost);
            inputs = {};
            const t = types.find((x) => x.type === type.value);
            for (const f of t ? t.fields : []) {
                const id = `nf-${f.key}`;
                const current = existing && existing.type === type.value ? existing.config[f.key] : '';
                const input = h('input', {
                    id, type: f.secret ? 'password' : 'text', autocomplete: 'off',
                    value: f.secret ? '' : (current || ''), placeholder: f.secret && current === MASK ? 'Unchanged' : '',
                });
                inputs[f.key] = { input, secret: f.secret, hadSecret: current === MASK };
                fieldsHost.append(h('div', { class: 'field' }, h('label', { for: id }, f.label, f.required ? ' *' : ''), input));
            }
        }
        paintFields();

        const saveBtn = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save');
        return h('form', {
            class: 'notifier-form', dataset: { testid: 'notifier-form' }, novalidate: true,
            onsubmit: async (e) => {
                e.preventDefault();
                error.hidden = true;
                const config = {};
                for (const [key, f] of Object.entries(inputs)) {
                    const v = f.input.value;
                    config[key] = f.secret && v === '' && f.hadSecret ? MASK : v;
                }
                saveBtn.setAttribute('aria-busy', 'true');
                try {
                    await save({ id: existing ? existing.id : 0, name: name.value, type: type.value, enabled: enabled.checked, config }, true);
                } catch (err) {
                    error.textContent = err.message;
                    error.hidden = false;
                } finally {
                    saveBtn.removeAttribute('aria-busy');
                }
            },
        },
            h('div', { class: 'field' }, h('label', { for: 'n-name' }, 'Name'), name),
            h('div', { class: 'field' }, h('label', { for: 'n-type' }, 'Type'), type),
            fieldsHost,
            h('label', { class: 'check', for: 'n-enabled' }, enabled, 'Enabled'),
            h('div', { class: 'row-flex form-actions' }, saveBtn, h('button', { class: 'btn', type: 'button', onclick: () => renderNotifiers() }, 'Cancel')),
            error);
    }

    async function save(n, throwOnError) {
        const body = { name: n.name, type: n.type, enabled: n.enabled, config: n.config };
        try {
            if (n.id) await api.put(`/api/notifiers/${n.id}`, body);
            else await api.post('/api/notifiers', body);
        } catch (e) {
            if (throwOnError) throw e;
            toast(e.message, 'error');
            await loadNotifiers();
            return;
        }
        await loadNotifiers();
    }

    async function loadNotifiers() {
        notifiers = await api.get('/api/notifiers');
        if (alive) renderNotifiers();
    }

    async function testSend(n) {
        try {
            await api.post(`/api/notifiers/${n.id}/test`);
            toast(`Test message sent to ${n.name}`, 'success');
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    async function remove(n) {
        const ok = await confirmDialog({ title: `Delete ${n.name}?`, body: h('p', {}, 'You will stop receiving messages through this notifier.'), confirmLabel: 'Delete', danger: true });
        if (!ok) return;
        try {
            await api.del(`/api/notifiers/${n.id}`);
            await loadNotifiers();
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    // ---- widget ----
    function renderWidget() {
        clear(widgetHost);
        const url = `${location.origin}/api/widget`;
        append(widgetHost, [
            h('p', { class: 'muted' }, 'Point a nextdash custom widget at this address and send the token as a Bearer token. It returns the number of updates, the number of breaking updates and the time of the last check.'),
            h('p', {}, h('code', {}, url)),
            h('div', { class: 'row-flex' },
                h('code', { class: 'token', dataset: { testid: 'widget-token' } }, tokenShown ? token : '•'.repeat(24)),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => { tokenShown = !tokenShown; renderWidget(); } }, tokenShown ? 'Hide' : 'Show'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: copyToken }, 'Copy token'),
                h('span', { class: 'spacer' }),
                h('button', { class: 'btn btn-small', type: 'button', onclick: rotate }, 'Create a new token'))]);
    }

    async function copyToken() {
        try {
            await navigator.clipboard.writeText(token);
            toast('Token copied', 'success');
        } catch {
            toast("Couldn't copy. Show the token and copy it by hand.", 'error');
        }
    }

    async function rotate() {
        const ok = await confirmDialog({ title: 'Create a new widget token?', body: h('p', {}, 'The current token stops working right away. Update your nextdash widget with the new one.'), confirmLabel: 'Create token' });
        if (!ok) return;
        try {
            token = (await api.post('/api/widget/token/rotate')).token;
            toast('New widget token created', 'success');
            renderWidget();
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    // ---- account ----
    function renderAccount() {
        clear(accountHost).append(h('div', { class: 'row-flex' },
            h('span', {}, `Signed in as ${me.name}`), h('span', { class: 'spacer' }),
            h('button', {
                class: 'btn', type: 'button',
                onclick: async () => {
                    try { await api.post('/api/logout'); } catch { /* cleared either way */ }
                    document.dispatchEvent(new CustomEvent('nu:signed-out'));
                },
            }, 'Sign out')));
    }

    (async () => {
        try {
            [types, me, token] = await Promise.all([api.get('/api/notifiers/types'), api.get('/api/me'), api.get('/api/widget/token').then((r) => r.token)]);
            if (!alive) return;
            await loadNotifiers();
            renderWidget();
            renderAccount();
        } catch (e) {
            if (alive && e.status !== 401) notifiersHost.replaceChildren(h('p', { class: 'error-text' }, e.message));
        }
    })();

    return { unmount() { alive = false; }, extraHost };
}
