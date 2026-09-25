import test from 'node:test';
import assert from 'node:assert/strict';
import { parseVersion, versionChange, relativeTime, shortImageId, plural, outcomeLabel } from '../internal/web/static/js/format.js';

test('parseVersion', () => {
    const v = (major, minor, patch, extra = {}) => ({ major, minor, patch, rev: 0, build: 0, pre: '', ...extra });
    assert.deepEqual(parseVersion('v1.2.3'), v(1, 2, 3));
    assert.deepEqual(parseVersion('2'), v(2, 0, 0));
    assert.deepEqual(parseVersion('1.2'), v(1, 2, 0));
    assert.deepEqual(parseVersion('1.2.3-rc.1+build5'), v(1, 2, 3, { pre: 'rc.1' }));
    // linuxserver.io style: a fourth number and an image build suffix
    assert.deepEqual(parseVersion('4.0.20.3014-ls325'), v(4, 0, 20, { rev: 3014, build: 325 }));
    assert.deepEqual(parseVersion('1.26.3-r0-ls345'), v(1, 26, 3, { build: 345 }));
    assert.deepEqual(parseVersion('1.2.3-ls'), v(1, 2, 3, { pre: 'ls' }));
    assert.deepEqual(parseVersion('1.2.3.4'), v(1, 2, 3, { rev: 4 }));
    for (const bad of ['', 'latest', '1.2.3.4.5', '1.x.3', undefined, null]) assert.equal(parseVersion(bad), null, String(bad));
});

test('versionChange', () => {
    const cases = [
        ['1.2.3', '1.2.3', 'same'], ['1.2.3', '1.2.4', 'patch'], ['1.2.3', '1.3.0', 'minor'],
        ['1.2.3', '2.0.0', 'major'], ['1.2.3', '1.2.2', 'downgrade'], ['0.4.1', '0.4.2', 'patch'],
        ['0.4.1', '0.5.0', 'major'], ['', '1.0.0', ''], ['1.0.0', 'latest', ''],
        ['4.0.20.3014-ls325', '4.0.20.3014-ls326', 'patch'], // the image was rebuilt
        ['4.0.20.3014-ls325', '4.0.21.3020-ls326', 'patch'],
        ['4.0.20.3014-ls325', '4.1.0.1-ls1', 'minor'],
        ['4.0.20.3014-ls325', '5.0.0.1-ls1', 'major'],
        ['4.0.20.3020-ls9', '4.0.20.3014-ls325', 'downgrade'],
        ['4.0.20.3014-ls325', '4.0.20.3014-ls325', 'same'],
    ];
    for (const [a, b, want] of cases) assert.equal(versionChange(a, b), want, `${a} to ${b}`);
});

test('relativeTime', () => {
    const now = Date.parse('2026-09-25T12:00:00Z');
    const at = (ms) => new Date(now - ms).toISOString();
    assert.equal(relativeTime(at(20_000), now), 'just now');
    assert.equal(relativeTime(at(60_000), now), '1 minute ago');
    assert.equal(relativeTime(at(5 * 60_000), now), '5 minutes ago');
    assert.equal(relativeTime(at(3_600_000), now), '1 hour ago');
    assert.equal(relativeTime(at(3 * 86_400_000), now), '3 days ago');
    assert.match(relativeTime(at(60 * 86_400_000), now), /^\d{1,2} \w{3} 2026$/);
    assert.equal(relativeTime(new Date(now + 60_000).toISOString(), now), '');
    assert.equal(relativeTime('nonsense', now), '');
    assert.equal(relativeTime('', now), '');
});

test('small helpers', () => {
    assert.equal(shortImageId('sha256:0123456789abcdef0123'), '0123456789ab');
    assert.equal(shortImageId(''), '');
    assert.equal(plural(1, 'update', 'updates'), '1 update');
    assert.equal(plural(3, 'update', 'updates'), '3 updates');
    assert.equal(outcomeLabel('ok'), 'Updated');
    assert.equal(outcomeLabel('rolled_back'), 'Rolled back');
    assert.equal(outcomeLabel('failed'), 'Failed');
    assert.equal(outcomeLabel('other'), 'other');
});
