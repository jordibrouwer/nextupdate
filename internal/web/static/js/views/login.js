import { h, clear } from '../dom.js';
import { api } from '../api.js';

/** mode is 'setup' or 'login'. onDone runs after a session exists. */
export function renderAuth(root, mode, onDone) {
    const setup = mode === 'setup';
    const error = h('p', { class: 'error-text', dataset: { testid: 'auth-error' }, hidden: true });
    const name = h('input', { id: 'auth-name', type: 'text', autocomplete: 'username', required: true, autofocus: true });
    const password = h('input', { id: 'auth-password', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', required: true });
    const submit = h('button', { class: 'btn btn-primary', type: 'submit' }, setup ? 'Create account' : 'Sign in');

    const form = h('form', {
        dataset: { testid: 'auth-form' },
        onsubmit: async (e) => {
            e.preventDefault();
            error.hidden = true;
            submit.setAttribute('aria-busy', 'true');
            try {
                await api.post(setup ? '/api/setup' : '/api/login', { name: name.value, password: password.value });
                onDone();
            } catch (err) {
                error.textContent = err.message;
                error.hidden = false;
            } finally {
                submit.removeAttribute('aria-busy');
            }
        },
    },
        h('div', { class: 'field' }, h('label', { for: 'auth-name' }, 'Name'), name),
        h('div', { class: 'field' }, h('label', { for: 'auth-password' }, 'Password'), password,
            setup ? h('p', { class: 'hint' }, 'Use at least 10 characters.') : null),
        submit,
        error);

    clear(root).append(h('main', { class: 'auth' },
        h('div', { class: 'card' },
            h('h1', {}, setup ? 'Create your admin account' : 'Sign in'),
            h('p', { class: 'lede' }, setup ? 'This account controls updates for the containers on this host.' : 'Sign in to manage container updates.'),
            form)));
    name.focus();
}
