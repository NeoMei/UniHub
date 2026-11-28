import { expireCreditBatches } from './billing/credits.js';
import { processPendingOutbox } from './billing/orders.js';
import { processRenewalCycle } from './billing/subscriptions.js';
import { reconcileExpiredHolds } from './gateway/billing-v1.js';

export function startBackgroundWorkers(pool, {
  region,
  currency,
  lagoConfig = {},
  workerConfig = {},
  logger = console,
}) {
  const inFlight = new Set();
  const timers = [];

  async function runGuarded(name, fn) {
    if (inFlight.has(name)) return;
    inFlight.add(name);
    try {
      await fn();
    } catch (error) {
      logger?.error?.(`UniHub worker [${name}] error:`, error?.message ?? error);
    } finally {
      inFlight.delete(name);
    }
  }

  function schedule(name, intervalMs, fn) {
    const timer = setInterval(() => { void runGuarded(name, fn); }, intervalMs);
    timer.unref?.();
    timers.push(timer);
  }

  const jobs = {
    outbox: () => processPendingOutbox(pool, { limit: 25, lagoConfig }),
    holdReconcile: () => reconcileExpiredHolds(pool),
    creditExpire: () => expireCreditBatches(pool),
    renewals: () => processRenewalCycle(pool, { region, currency }),
  };

  if (workerConfig.enabled !== false) {
    schedule('outbox', workerConfig.outboxIntervalMs ?? 5000, jobs.outbox);
    schedule('holdReconcile', workerConfig.holdReconcileIntervalMs ?? 30_000, jobs.holdReconcile);
    schedule('creditExpire', workerConfig.creditExpireIntervalMs ?? 60_000, jobs.creditExpire);
    schedule('renewals', workerConfig.renewalIntervalMs ?? 60_000, jobs.renewals);
  }

  return {
    async runOnce() {
      await runGuarded('outbox', jobs.outbox);
      await runGuarded('holdReconcile', jobs.holdReconcile);
      await runGuarded('creditExpire', jobs.creditExpire);
      await runGuarded('renewals', jobs.renewals);
    },
    stop() {
      for (const timer of timers) clearInterval(timer);
      timers.length = 0;
    },
  };
}
