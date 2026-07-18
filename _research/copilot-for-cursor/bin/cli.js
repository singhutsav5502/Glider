#!/usr/bin/env node
const { execSync, spawn } = require('child_process');
const path = require('path');
const fs = require('fs');

// Check if bun is installed
let bunPath;
try {
    bunPath = execSync('where bun', { encoding: 'utf-8', stdio: ['pipe', 'pipe', 'pipe'] }).trim().split('\n')[0];
} catch {
    try {
        bunPath = execSync('which bun', { encoding: 'utf-8', stdio: ['pipe', 'pipe', 'pipe'] }).trim();
    } catch {
        console.error('❌ Bun is required but not installed.');
        console.error('   Install it with: curl -fsSL https://bun.sh/install | bash');
        console.error('   Or: npm install -g bun');
        process.exit(1);
    }
}

// Find start.ts relative to this script
const pkgRoot = path.resolve(__dirname, '..');
const startScript = path.join(pkgRoot, 'start.ts');

if (!fs.existsSync(startScript)) {
    console.error('❌ start.ts not found at:', startScript);
    process.exit(1);
}

// Forward all args and run with bun
const args = ['run', startScript, ...process.argv.slice(2)];
const child = spawn(bunPath.trim(), args, {
    stdio: 'inherit',
    cwd: pkgRoot,
    env: { ...process.env },
});

child.on('error', (err) => {
    console.error('❌ Failed to start:', err.message);
    process.exit(1);
});

child.on('exit', (code) => {
    process.exit(code || 0);
});

// Forward signals for graceful shutdown
process.on('SIGINT', () => child.kill('SIGINT'));
process.on('SIGTERM', () => child.kill('SIGTERM'));
