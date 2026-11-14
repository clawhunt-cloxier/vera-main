// Fails when src/api/schema.d.ts differs from what openapi-typescript generates from contracts/openapi.json.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const committed = 'src/api/schema.d.ts';
const dir = mkdtempSync(join(tmpdir(), 'vera-api-types-'));
try {
  const out = join(dir, 'schema.d.ts');
  execFileSync(
    'npx',
    ['--no-install', 'openapi-typescript', '../contracts/openapi.json', '-o', out],
    {
      stdio: ['ignore', 'ignore', 'inherit'],
    }
  );
  if (readFileSync(out, 'utf8') !== readFileSync(committed, 'utf8')) {
    console.error(
      `${committed} is out of date with contracts/openapi.json; run: cd web && npm run gen:api`
    );
    process.exit(1);
  }
  console.log(`${committed} matches contracts/openapi.json`);
} finally {
  rmSync(dir, { recursive: true, force: true });
}
