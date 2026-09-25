// A small, safe Markdown reader for release notes. It never produces HTML:
// parseMarkdown returns a plain tree, and renderMarkdown builds DOM nodes
// from it. Raw HTML in the source stays visible text.
import { h } from './dom.js';

const SAFE_URL = /^https?:\/\/[^\s<>"]+$/i;

const INLINE = new RegExp([
    '(`+)([\\s\\S]*?[^`])\\1(?!`)',                 // 1,2: code
    '\\*\\*([\\s\\S]+?)\\*\\*',                     // 3: strong
    '__([\\s\\S]+?)__',                             // 4: strong
    '\\*([^\\s*][\\s\\S]*?)\\*',                    // 5: em
    '(?<![A-Za-z0-9])_([^\\s_][\\s\\S]*?)_(?![A-Za-z0-9])', // 6: em
    '\\[([^\\]]+)\\]\\(([^)\\s]+)\\)',              // 7,8: link
].join('|'));

export function parseInline(src) {
    const out = [];
    let rest = String(src ?? '');
    const pushText = (v) => {
        if (!v) return;
        const last = out[out.length - 1];
        if (last && last.t === 'text') last.v += v;
        else out.push({ t: 'text', v });
    };
    while (rest) {
        const m = INLINE.exec(rest);
        if (!m) { pushText(rest); break; }
        pushText(rest.slice(0, m.index));
        if (m[2] !== undefined) out.push({ t: 'code', v: m[2].trim() });
        else if (m[3] !== undefined) out.push({ t: 'strong', c: parseInline(m[3]) });
        else if (m[4] !== undefined) out.push({ t: 'strong', c: parseInline(m[4]) });
        else if (m[5] !== undefined) out.push({ t: 'em', c: parseInline(m[5]) });
        else if (m[6] !== undefined) out.push({ t: 'em', c: parseInline(m[6]) });
        else if (SAFE_URL.test(m[8])) out.push({ t: 'link', href: m[8], c: parseInline(m[7]) });
        else pushText(m[0]); // unsafe or odd link: keep the source text
        rest = rest.slice(m.index + m[0].length);
    }
    return out;
}

const FENCE = /^(`{3,}|~{3,})\s*\S*\s*$/;
const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const HR = /^\s*([-*_])(\s*\1){2,}\s*$/;
const UL = /^\s*[-*+]\s+(.*)$/;
const OL = /^\s*\d+[.)]\s+(.*)$/;
const QUOTE = /^>\s?(.*)$/;

const startsBlock = (line) => FENCE.test(line) || HEADING.test(line) || HR.test(line) || UL.test(line) || OL.test(line) || QUOTE.test(line);

export function parseMarkdown(src) {
    if (typeof src !== 'string') return [];
    const lines = src.replace(/\r\n?/g, '\n').replace(/<!--[\s\S]*?-->\n?/g, '').split('\n');
    const blocks = [];
    let i = 0;
    while (i < lines.length) {
        const line = lines[i];
        let m;
        if (/^\s*$/.test(line)) { i++; continue; }
        if ((m = FENCE.exec(line))) {
            const fence = m[1];
            const body = [];
            i++;
            while (i < lines.length && !lines[i].trim().startsWith(fence)) body.push(lines[i++]);
            i++; // closing fence
            blocks.push({ t: 'code', v: body.join('\n') });
        } else if ((m = HEADING.exec(line))) {
            blocks.push({ t: 'heading', level: m[1].length, c: parseInline(m[2]) });
            i++;
        } else if (HR.test(line)) {
            blocks.push({ t: 'hr' });
            i++;
        } else if (QUOTE.test(line)) {
            const body = [];
            while (i < lines.length && QUOTE.test(lines[i])) body.push(QUOTE.exec(lines[i++])[1]);
            blocks.push({ t: 'quote', c: parseMarkdown(body.join('\n')) });
        } else if (UL.test(line) || OL.test(line)) {
            const ordered = OL.test(line);
            const re = ordered ? OL : UL;
            const items = [];
            while (i < lines.length && re.test(lines[i])) items.push(parseInline(re.exec(lines[i++])[1]));
            blocks.push({ t: ordered ? 'ol' : 'ul', items });
        } else {
            const para = [];
            while (i < lines.length && !/^\s*$/.test(lines[i]) && (para.length === 0 || !startsBlock(lines[i]))) para.push(lines[i++].trim());
            blocks.push({ t: 'p', c: parseInline(para.join(' ')) });
        }
    }
    return blocks;
}

function renderInline(nodes) {
    return nodes.map((n) => {
        switch (n.t) {
            case 'code': return h('code', {}, n.v);
            case 'strong': return h('strong', {}, renderInline(n.c));
            case 'em': return h('em', {}, renderInline(n.c));
            case 'link': return h('a', { href: n.href, target: '_blank', rel: 'noopener noreferrer' }, renderInline(n.c));
            default: return n.v;
        }
    });
}

function renderBlock(b) {
    switch (b.t) {
        case 'heading': return h(`h${Math.min(b.level + 3, 6)}`, {}, renderInline(b.c));
        case 'ul': return h('ul', {}, b.items.map((i) => h('li', {}, renderInline(i))));
        case 'ol': return h('ol', {}, b.items.map((i) => h('li', {}, renderInline(i))));
        case 'code': return h('pre', {}, h('code', {}, b.v));
        case 'quote': return h('blockquote', {}, b.c.map(renderBlock));
        case 'hr': return h('hr');
        default: return h('p', {}, renderInline(b.c));
    }
}

/** Builds DOM for release notes. Only nodes made here reach the page. */
export function renderMarkdown(src) {
    const frag = document.createDocumentFragment();
    for (const b of parseMarkdown(src)) frag.append(renderBlock(b));
    return frag;
}
