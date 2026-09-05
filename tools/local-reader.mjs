#!/usr/bin/env node
// Run from any directory. Ctrl-C stops only these child processes; data persists.
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { createServer } from 'node:net';

const root = fileURLToPath(new URL('../', import.meta.url));
const data = resolve(root, 'data/reader-design');
mkdirSync(data, { recursive: true, mode: 0o700 });
const settingsPath = resolve(data, 'local-access.json');
if (!existsSync(settingsPath)) writeFileSync(settingsPath, JSON.stringify({
	secret: randomBytes(32).toString('base64'), password: randomBytes(18).toString('base64url')
}, null, 2), { mode: 0o600, flag: 'wx' });
const access = JSON.parse(readFileSync(settingsPath, 'utf8'));
const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('DOUBLANGU_')));
const env = { ...environment, DOUBLANGU_SECRET: access.secret,
	DOUBLANGU_DB_PATH: resolve(data, 'doublangu.db'), DOUBLANGU_DATA_PATH: data,
	DOUBLANGU_MEDIA_PATH: resolve(data, 'media'), DOUBLANGU_ANNOTATOR: 'disabled',
	DOUBLANGU_LISTEN: '127.0.0.1:8097', DOUBLANGU_PUBLIC_URL: 'http://127.0.0.1:5177',
	DOUBLANGU_DEV_API_TARGET: 'http://127.0.0.1:8097', DOUBLANGU_READER_DEMO: '1' };
for (const port of [8097, 5177]) await new Promise((accept, reject) => {
	const probe = createServer(); probe.once('error', reject);
	probe.listen(port, '127.0.0.1', () => probe.close(accept));
});
const binary = resolve(data, 'doublangu-server');
const build = spawnSync('go', ['build', '-o', binary, './cmd/doublangu-server'], { cwd: root, env, stdio: 'inherit' });
if (build.status !== 0) process.exit(build.status || 1);
if (!existsSync(env.DOUBLANGU_DB_PATH)) {
	const owner = spawnSync(binary, ['--create-owner'], { cwd: root, env, input: `${access.password}\n`, encoding: 'utf8' });
	if (owner.status !== 0) { console.error(owner.stderr); process.exit(owner.status || 1); }
}
const children = [];
let stopping = false;
function stop() { if (stopping) return; stopping = true; for (const child of children) child.kill('SIGTERM'); }
process.on('SIGINT', stop); process.on('SIGTERM', stop);
const api = spawn(binary, [], { cwd: root, env, stdio: 'inherit' }); children.push(api);
const web = spawn(process.execPath, [resolve(root, 'web/node_modules/vite/bin/vite.js'), '--host', '127.0.0.1', '--port', '5177', '--strictPort'], { cwd: resolve(root, 'web'), env, stdio: 'inherit' }); children.push(web);
for (const child of children) {
	child.on('error', error => { console.error(error.message); process.exitCode = 1; stop(); });
	child.on('exit', code => { if (!stopping && code !== 0) process.exitCode = code || 1; stop(); });
}
console.log('Reader: http://127.0.0.1:5177/reader/01J00000000000000000000LONG');
console.log(`Local sign-in password: ${access.password}`);
console.log(`Data and access settings: ${data}`);
console.log('Synthetic article; analysis disabled; production and Mac workers are not connected.');
