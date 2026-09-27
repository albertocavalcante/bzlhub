import { readdir, stat } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../build/_app/immutable/', import.meta.url));
const limits = {
  entry: 150 * 1024,
  nodes: 100 * 1024,
  chunks: 850 * 1024,
  total: 8 * 1024 * 1024,
};

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((entry) => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? files(path) : [path];
  }));
  return nested.flat();
}

const jsFiles = (await files(root)).filter((file) => file.endsWith('.js'));
let total = 0;
const failures = [];
for (const file of jsFiles) {
  const size = (await stat(file)).size;
  total += size;
  const path = relative(root, file);
  const category = path.startsWith('entry/')
    ? 'entry'
    : path.startsWith('nodes/')
      ? 'nodes'
      : 'chunks';
  if (size > limits[category]) {
    failures.push(`${path}: ${size} bytes > ${limits[category]} byte ${category} budget`);
  }
}
if (total > limits.total) {
  failures.push(`total JavaScript: ${total} bytes > ${limits.total} byte budget`);
}
if (failures.length > 0) {
  throw new Error(`bundle budget exceeded:\n${failures.join('\n')}`);
}
console.log(`bundle budget OK: ${jsFiles.length} files, ${total} bytes total`);
