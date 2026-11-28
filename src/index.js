import pg from 'pg';
import { buildApp } from './app.js';
import { readConfig } from './config.js';
import { startBackgroundWorkers } from './worker.js';

const config = readConfig(process.env);
const pool = new pg.Pool({ connectionString: config.databaseUrl, connectionTimeoutMillis: 5000 });
pool.on('error', (error) => console.error('PostgreSQL idle connection error', error.code ?? 'unknown'));

let app;
let workers;
try {
  app = await buildApp({ ...config, pool });
  workers = startBackgroundWorkers(pool, {
    region: config.region,
    currency: config.currency,
    lagoConfig: config.lagoConfig,
    workerConfig: config.workerConfig,
    logger: app.log,
  });
  app.addHook('onClose', async () => {
    workers?.stop();
    await pool.end();
  });
  await app.listen({ host: config.host, port: config.port });
} catch (error) {
  workers?.stop();
  if (app) await app.close();
  else await pool.end();
  throw error;
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, async () => { await app.close(); });
}
