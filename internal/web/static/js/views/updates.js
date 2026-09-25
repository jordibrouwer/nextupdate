import { h } from '../dom.js';

export function mountUpdates(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'Updates')));
    return { unmount() {} };
}
