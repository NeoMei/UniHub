import { randomUUID } from 'node:crypto';
import pg from 'pg';
import { readConfig } from '../config.js';
import { migrateDatabase } from '../database.js';
import { seedDocFlow } from './docflow.js';
import { seedSuperWagie } from './superwagie.js';

const config = readConfig(process.env);
const pool = new pg.Pool({ connectionString: config.databaseUrl, connectionTimeoutMillis: 5000 });

try {
  await migrateDatabase(pool, config.region, config.currency);

  const operatorSubject = [...config.operatorSubjects][0] ?? 'bootstrap-operator';
  const { rows: [existingUser] } = await pool.query(
    'SELECT user_id FROM oidc_identities WHERE issuer = $1 AND subject = $2',
    [config.issuer.href, operatorSubject],
  );
  let operatorUserId = existingUser?.user_id;
  if (!operatorUserId) {
    const newId = randomUUID();
    await pool.query(
      'INSERT INTO users (id, name, email) VALUES ($1, $2, $3)',
      [newId, 'Platform Seed Operator', 'seed@unihub.invalid'],
    );
    await pool.query(
      'INSERT INTO oidc_identities (issuer, subject, user_id) VALUES ($1, $2, $3)',
      [config.issuer.href, operatorSubject, newId],
    );
    await pool.query(
      "INSERT INTO accounts (id, kind, name, owner_id) VALUES ($1, 'personal', $2, $3)",
      [randomUUID(), 'Seed Operator Personal', newId],
    );
    operatorUserId = newId;
  }

  const sw = await seedSuperWagie(pool, {
    operatorUserId, region: config.region, currency: config.currency,
  });
  const df = await seedDocFlow(pool, {
    operatorUserId, region: config.region, currency: config.currency,
  });

  console.log(JSON.stringify({
    ok: true,
    region: config.region,
    currency: config.currency,
    seeded: [
      { id: sw.app.id, name: sw.app.name },
      { id: df.app.id, name: df.app.name },
    ],
  }, null, 2));
} finally {
  await pool.end();
}
