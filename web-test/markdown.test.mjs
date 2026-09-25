import test from 'node:test';
import assert from 'node:assert/strict';
import { parseMarkdown, parseInline } from '../internal/web/static/js/markdown.js';

const text = (v) => ({ t: 'text', v });

test('paragraphs and soft line breaks', () => {
    assert.deepEqual(parseMarkdown('one\ntwo\n\nthree'), [
        { t: 'p', c: [text('one two')] },
        { t: 'p', c: [text('three')] },
    ]);
});

test('headings', () => {
    assert.deepEqual(parseMarkdown('## What changed ##'), [{ t: 'heading', level: 2, c: [text('What changed')] }]);
});

test('lists', () => {
    assert.deepEqual(parseMarkdown('- a\n- b **bold**\n* c'), [{ t: 'ul', items: [[text('a')], [text('b '), { t: 'strong', c: [text('bold')] }], [text('c')]] }]);
    assert.deepEqual(parseMarkdown('1. x\n2) y'), [{ t: 'ol', items: [[text('x')], [text('y')]] }]);
});

test('fenced code keeps its text and is never parsed', () => {
    assert.deepEqual(parseMarkdown('```yaml\n**not bold**\n<b>x</b>\n```'), [{ t: 'code', v: '**not bold**\n<b>x</b>' }]);
});

test('quotes nest and hr', () => {
    assert.deepEqual(parseMarkdown('> Note: careful\n\n---'), [{ t: 'quote', c: [{ t: 'p', c: [text('Note: careful')] }] }, { t: 'hr' }]);
});

test('inline: code, emphasis, links', () => {
    assert.deepEqual(parseInline('use `x` and *this* or _that_'), [text('use '), { t: 'code', v: 'x' }, text(' and '), { t: 'em', c: [text('this')] }, text(' or '), { t: 'em', c: [text('that')] }]);
    assert.deepEqual(parseInline('[docs](https://example.com/a?b=1)'), [{ t: 'link', href: 'https://example.com/a?b=1', c: [text('docs')] }]);
});

test('untrusted markup stays text', () => {
    assert.deepEqual(parseMarkdown('<script>alert(1)</script>'), [{ t: 'p', c: [text('<script>alert(1)</script>')] }]);
    assert.deepEqual(parseInline('<img src=x onerror=alert(1)>'), [text('<img src=x onerror=alert(1)>')]);
});

test('only http and https links become links', () => {
    for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,x', 'vbscript:x', '//evil.example', '/relative', 'mailto:a@b.c']) {
        const got = parseInline(`[click](${bad})`);
        assert.ok(got.every((n) => n.t !== 'link'), `${bad} must not become a link: ${JSON.stringify(got)}`);
        assert.equal(got.map((n) => n.v ?? '').join(''), `[click](${bad})`);
    }
    assert.equal(parseInline('[a](http://example.com)')[0].t, 'link');
});

test('html comments are dropped', () => {
    assert.deepEqual(parseMarkdown('before\n<!-- hidden -->\nafter'), [{ t: 'p', c: [text('before after')] }]);
});

test('empty and whitespace input', () => {
    assert.deepEqual(parseMarkdown(''), []);
    assert.deepEqual(parseMarkdown('  \n\n '), []);
    assert.deepEqual(parseMarkdown(null), []);
});
