import { h } from '../dom.js';

export function mountSettings(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'Settings')));
    return { unmount() {} };
}
