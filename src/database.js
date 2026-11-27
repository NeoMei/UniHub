import { readdir, readFile } from 'node:fs/promises';
import { validateRegion } from './config.js';

export function isUuid(value) {
  return typeof value === 'string' && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(value);
}

export async function transaction(pool, run) {
  const connection = await pool.connect();
  try {
    await connection.query('BEGIN');
    const result = await run(connection);
    await connection.query('COMMIT');
    return result;
  } catch (error) {
    await connection.query('ROLLBACK');
    throw error;
  } finally {
    connection.release();
  }
}

export async function migrateDatabase(pool, region, currency) {
  validateRegion(region, currency);
  const directory = new URL('../migrations/', import.meta.url);
  const files = (await readdir(directory)).filter((name) => /^\d{3}_.+\.sql$/.test(name)).sort();
  await transaction(pool, async (connection) => {
    await connection.query('SELECT pg_advisory_xact_lock(46028, 1)');
    await connection.query('CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY)');
    const applied = new Set((await connection.query('SELECT name FROM schema_migrations')).rows.map(({ name }) => name));
    for (const file of files) {
      if (applied.has(file)) continue;
      await connection.query(await readFile(new URL(file, directory), 'utf8'));
      await connection.query('INSERT INTO schema_migrations (name) VALUES ($1)', [file]);
    }
    await connection.query(`
      INSERT INTO deployment_region (region, currency) VALUES ($1, $2)
      ON CONFLICT (singleton) DO NOTHING`, [region, currency]);
    const { rows: [bound] } = await connection.query('SELECT region, currency FROM deployment_region');
    if (bound.region !== region || bound.currency !== currency) {
      throw new Error('Database region and currency do not match the deployment configuration');
    }
  });
}
