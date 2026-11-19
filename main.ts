import { spawn } from 'node:child_process';
import { chmodSync } from 'node:fs';

const envUrl = (process.env.MAKEFUN_ENV_URL ?? '').replace(/\/+$/, '');
if (!envUrl) {
  process.stderr.write('MAKEFUN_ENV_URL is required; no forge environment is available.\n');
  process.exit(1);
}

try { chmodSync('./forge', 0o755); } catch {}
const child = spawn('./forge', [], { stdio: ['ignore', 'pipe', 'inherit'] });
let exited = false;
child.once('exit', () => { exited = true; });
const port = await new Promise((resolve, reject) => {
  let data = '';
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', chunk => {
    data += chunk;
    const newline = data.indexOf('\n');
    if (newline >= 0) {
      const match = /^FORGE_PORT=(\d+)$/.exec(data.slice(0, newline).trim());
      if (match) resolve(Number(match[1]));
      else reject(new Error('forge did not announce its port'));
    }
  });
  child.once('error', reject);
  child.once('exit', code => reject(new Error(`forge exited before startup (${code})`)));
});

const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
async function retry(operation) {
  let delay = 250;
  for (;;) {
    if (exited) throw new Error('forge exited while waiting for the environment');
    try { if (await operation()) return; } catch {}
    await wait(delay);
    delay = Math.min(delay * 2, 5000);
  }
}
try {
  await retry(async () => {
    const response = await fetch(`${envUrl}/register`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ port }) });
    return response.status === 409 || response.ok;
  });
  await retry(async () => {
    const response = await fetch(`${envUrl}/done`);
    if (!response.ok) return false;
    const state = await response.json();
    return state?.done === true;
  });
  child.kill('SIGTERM');
  await new Promise(resolve => child.once('exit', resolve));
  process.exit(0);
} catch (error) {
  process.stderr.write(`Forge lifecycle failed: ${error instanceof Error ? error.message : String(error)}\n`);
  child.kill('SIGTERM');
  process.exit(1);
}
