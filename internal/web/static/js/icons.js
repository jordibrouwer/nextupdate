const NS = 'http://www.w3.org/2000/svg';
const PATHS = {
    refresh: ['M21 12a9 9 0 1 1-3-6.7', 'M21 3v6h-6'],
    check: ['M20 6 9 17l-5-5'],
    alert: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z', 'M12 8v5', 'M12 16h.01'],
    back: ['M15 18l-6-6 6-6'],
    external: ['M14 4h6v6', 'M20 4 10 14', 'M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5'],
    contrast: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z', 'M12 3v18'],
    box: ['M21 8l-9-5-9 5v8l9 5 9-5z', 'M3 8l9 5 9-5', 'M12 13v8'],
};

export function icon(name, size = 16) {
    const svg = document.createElementNS(NS, 'svg');
    const attrs = { viewBox: '0 0 24 24', width: size, height: size, fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' };
    for (const [k, v] of Object.entries(attrs)) svg.setAttribute(k, v);
    svg.classList.add('icon');
    for (const d of PATHS[name] || []) {
        const p = document.createElementNS(NS, 'path');
        p.setAttribute('d', d);
        svg.append(p);
    }
    return svg;
}
