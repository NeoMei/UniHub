import { BillingError } from '../billing/errors.js';
import { transaction } from '../database.js';

export async function runReconciliation(pool, { region, currency }) {
  return transaction(pool, async (connection) => {
    const { rows: [regionRow] } = await connection.query(
      'SELECT region, currency FROM deployment_region WHERE singleton',
    );
    if (regionRow && (regionRow.region !== region || regionRow.currency !== currency)) {
      throw new BillingError('region_currency_mismatch', 400);
    }

    const { rows: cashMismatches } = await connection.query(`
      WITH ledger_totals AS (
        SELECT account_id, COALESCE(sum(delta_minor), 0)::bigint AS net_minor
        FROM cash_ledger_entries
        GROUP BY account_id
      )
      SELECT w.account_id AS "accountId", w.region, w.currency,
        w.available_minor::text AS "availableMinor",
        w.arrears_minor::text AS "arrearsMinor",
        (w.available_minor - w.arrears_minor)::text AS "netWalletMinor",
        COALESCE(l.net_minor, 0)::text AS "ledgerNetMinor"
      FROM wallet_accounts w
      LEFT JOIN ledger_totals l ON l.account_id = w.account_id
      WHERE w.region = $1 AND w.currency = $2
        AND (w.available_minor - w.arrears_minor) <> COALESCE(l.net_minor, 0)`,
    [region, currency]);

    const { rows: batchMismatches } = await connection.query(`
      WITH ledger_sums AS (
        SELECT batch_id,
          COALESCE(sum(delta_available_units), 0)::bigint AS sum_available,
          COALESCE(sum(delta_held_units), 0)::bigint AS sum_held,
          COALESCE(sum(delta_settled_units), 0)::bigint AS sum_settled,
          COALESCE(sum(delta_expired_units), 0)::bigint AS sum_expired
        FROM credit_ledger_entries
        GROUP BY batch_id
      )
      SELECT b.id AS "batchId", b.account_id AS "accountId", b.app_id AS "appId",
        b.granted_units::text AS "grantedUnits",
        b.available_units::text AS "availableUnits",
        b.held_units::text AS "heldUnits",
        b.settled_units::text AS "settledUnits",
        b.expired_units::text AS "expiredUnits",
        COALESCE(l.sum_available, 0)::text AS "ledgerAvailableUnits",
        COALESCE(l.sum_held, 0)::text AS "ledgerHeldUnits",
        COALESCE(l.sum_settled, 0)::text AS "ledgerSettledUnits",
        COALESCE(l.sum_expired, 0)::text AS "ledgerExpiredUnits"
      FROM credit_batches b
      LEFT JOIN ledger_sums l ON l.batch_id = b.id
      WHERE b.granted_units <> (b.available_units + b.held_units + b.settled_units + b.expired_units)
         OR b.available_units <> COALESCE(l.sum_available, 0)
         OR b.held_units <> COALESCE(l.sum_held, 0)
         OR b.settled_units <> COALESCE(l.sum_settled, 0)
         OR b.expired_units <> COALESCE(l.sum_expired, 0)`);

    const { rows: [expiredHoldsRow] } = await connection.query(`
      SELECT count(*)::int AS count
      FROM credit_holds
      WHERE status IN ('reserved', 'dispatching', 'in_flight', 'reconciling')
        AND expires_at <= statement_timestamp()`);

    const { rows: [totals] } = await connection.query(`
      SELECT
        count(*)::int AS "walletCount",
        COALESCE(sum(available_minor), 0)::text AS "totalAvailableMinor",
        COALESCE(sum(arrears_minor), 0)::text AS "totalArrearsMinor"
      FROM wallet_accounts
      WHERE region = $1 AND currency = $2`, [region, currency]);

    return {
      ok: cashMismatches.length === 0 && batchMismatches.length === 0,
      region,
      currency,
      checkedAt: new Date().toISOString(),
      walletCount: totals.walletCount,
      totalAvailableMinor: totals.totalAvailableMinor,
      totalArrearsMinor: totals.totalArrearsMinor,
      expiredUnresolvedHolds: expiredHoldsRow.count,
      cashMismatches,
      batchMismatches,
    };
  });
}
