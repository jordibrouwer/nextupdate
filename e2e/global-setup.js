const { execFileSync } = require('node:child_process');
const path = require('node:path');
const fs = require('node:fs');

// Build the demo server once, so every worker only has to start a binary.
module.exports = async () => {
    const root = path.resolve(__dirname, '..');
    fs.mkdirSync(path.join(root, 'e2e', '.bin'), { recursive: true });
    execFileSync('go', ['build', '-o', path.join(root, 'e2e', '.bin', 'uidemo'), './cmd/uidemo'], { cwd: root, stdio: 'inherit' });
};
