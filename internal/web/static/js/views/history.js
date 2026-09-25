import { h } from '../dom.js';

export function mountHistory(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'History')));
    return { unmount() {} };
}
