import { randomUUID } from 'node:crypto';
import { BillingError } from '../billing/errors.js';
import { registerOutboxHandler } from '../billing/orders.js';
import { transaction } from '../database.js';

async function postJson(url, method, headers, body) {
  const res = await fetch(url, {
    method,
    headers: { 'content-type': 'application/json', ...headers },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = { raw: text };
  }
  if (!res.ok) {
    throw new Error(`Lago HTTP ${res.status}: ${text.slice(0, 200)}`);
  }
  return json;
}

export async function syncOrderToLago(pool, orderId, lagoConfig = {}) {
  const { rows: [order] } = await pool.query(
    'SELECT * FROM commerce_orders WHERE id = $1', [orderId],
  );
  if (!order) throw new BillingError('order_not_found', 404);

  if (!lagoConfig?.enabled || !lagoConfig?.baseUrl) {
    return { status: 'skipped', orderId };
  }

  const { rows: [existing] } = await pool.query(
    'SELECT * FROM lago_sync_records WHERE order_id = $1', [orderId],
  );
  if (existing?.status === 'synced') {
    return {
      status: 'synced',
      duplicate: true,
      orderId,
      lagoInvoiceId: existing.lago_invoice_id,
    };
  }

  const headers = lagoConfig.apiKey ? { authorization: `Bearer ${lagoConfig.apiKey}` } : {};
  const baseUrl = String(lagoConfig.baseUrl).replace(/\/+$/, '');
  const unitCents = Math.round(Number(order.paid_minor) / Math.max(1, Number(order.quantity)));
  const invoicePayload = {
    invoice: {
      external_customer_id: order.account_id,
      currency: order.currency,
      skip_psp: true,
      fees: [
        {
          item_code: `unihub:${order.card_tier_id}`,
          external_unique_id: order.id,
          units: order.quantity,
          unit_amount_cents: unitCents,
          invoice_display_name: `${order.snapshot_json?.card?.display?.name ?? order.card_tier_id} (${order.order_no})`,
        },
      ],
    },
  };

  try {
    await postJson(`${baseUrl}/api/v1/customers`, 'POST', headers, {
      customer: {
        external_id: order.account_id,
        currency: order.currency,
        name: `UniHub Account ${order.account_id.slice(0, 8)}`,
      },
    });
    const invRes = await postJson(`${baseUrl}/api/v1/invoices`, 'POST', headers, invoicePayload);
    const lagoInvoiceId = invRes?.invoice?.lago_id ?? invRes?.invoice?.id ?? invRes?.lago_id ?? null;
    if (!lagoInvoiceId) throw new Error('Lago response missing invoice lago_id');

    const putRes = await postJson(`${baseUrl}/api/v1/invoices/${encodeURIComponent(lagoInvoiceId)}`, 'PUT', headers, {
      invoice: { payment_status: 'succeeded' },
    });

    await transaction(pool, async (connection) => {
      await connection.query(`
        INSERT INTO lago_sync_records (
          id, order_id, lago_customer_id, lago_invoice_id, status, payload_json, response_json, last_error, updated_at
        ) VALUES ($1, $2, $3, $4, 'synced', $5::jsonb, $6::jsonb, NULL, statement_timestamp())
        ON CONFLICT (order_id) DO UPDATE SET
          lago_invoice_id = EXCLUDED.lago_invoice_id,
          status = 'synced',
          payload_json = EXCLUDED.payload_json,
          response_json = EXCLUDED.response_json,
          last_error = NULL,
          updated_at = statement_timestamp()`, [
        randomUUID(), order.id, order.account_id, lagoInvoiceId,
        JSON.stringify(invoicePayload), JSON.stringify({ invRes, putRes }),
      ]);
      await connection.query(`
        UPDATE commerce_orders
        SET lago_invoice_id = $1, lago_sync_status = 'synced'
        WHERE id = $2`, [lagoInvoiceId, order.id]);
    });

    return { status: 'synced', duplicate: false, orderId, lagoInvoiceId };
  } catch (error) {
    const message = error instanceof Error ? error.message.slice(0, 500) : 'lago_sync_failed';
    await transaction(pool, async (connection) => {
      await connection.query(`
        INSERT INTO lago_sync_records (
          id, order_id, lago_customer_id, lago_invoice_id, status, payload_json, last_error, updated_at
        ) VALUES ($1, $2, $3, NULL, 'failed', $4::jsonb, $5, statement_timestamp())
        ON CONFLICT (order_id) DO UPDATE SET
          status = 'failed',
          last_error = EXCLUDED.last_error,
          updated_at = statement_timestamp()`, [
        randomUUID(), order.id, order.account_id, JSON.stringify(invoicePayload), message,
      ]);
      await connection.query(
        "UPDATE commerce_orders SET lago_sync_status = 'failed' WHERE id = $1 AND lago_sync_status <> 'synced'",
        [order.id],
      );
    });
    throw error;
  }
}

registerOutboxHandler('lago_invoice_sync', async (_connection, payload, { pool, lagoConfig }) => {
  await syncOrderToLago(pool, payload.orderId, lagoConfig);
});
