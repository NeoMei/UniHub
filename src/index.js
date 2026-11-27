import pg from 'pg';
import { buildApp } from './app.js';
import { readConfig } from './config.js';

const config = readConfig(process.env);
const pool = new pg.Pool({ connectionString: config.databaseUrl, connectionTimeoutMillis: 5000 });
pool.on('error', (error) => console.error('PostgreSQL idle connection error', error.code ?? 'unknown'));

let app;
try {
  app = await buildApp({ ...config, pool });
  app.addHook('onClose', () => pool.end());
  await app.listen({ host: config.host, port: config.port });
} catch (error) {
  if (app) await app.close();
  else await pool.end();
  throw error;
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, async () => { await app.close(); });
}
