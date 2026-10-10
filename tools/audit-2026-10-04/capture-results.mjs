import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const auditDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(auditDir, '../..');
const output = path.join(auditDir, 'results');
fs.mkdirSync(output, { recursive: true });

const observations = [
  { name: 'frontend-repro', command: process.execPath, args: [path.join(auditDir, 'frontend-repro.mjs')] },
  { name: 'backend-repro', command: 'go', args: ['run', './tools/audit-2026-10-04'] },
  { name: 'task-switch-race', command: 'go', args: ['run', '-race', './tools/audit-2026-10-04', '--task-switch-race'], race: true },
];

let failed = false;
for (const item of observations) {
  const result = spawnSync(item.command, item.args, {
    cwd: root,
    encoding: 'utf8',
    windowsHide: true,
    timeout: 60000,
    maxBuffer: 16 * 1024 * 1024,
    env: item.race ? { ...process.env, GORACE: 'halt_on_error=1' } : process.env,
  });
  const log = `${result.stdout || ''}${result.stderr || ''}\nProcess exit code: ${result.status}\n${result.error ? String(result.error) + '\n' : ''}`;
  fs.writeFileSync(path.join(output, `${item.name}.log`), log);
  const observed = !result.error && (item.race
    ? result.status !== 0 && log.includes('WARNING: DATA RACE')
    : result.status === 0);
  console.log(`${observed ? 'OBSERVED' : 'UNEXPECTED'} ${item.name}: exit ${result.status}`);
  failed ||= !observed;
}
console.log('These logs describe current defects; they are not repair acceptance tests.');
process.exitCode = failed ? 1 : 0;
