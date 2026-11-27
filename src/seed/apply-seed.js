import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { validateDraftInput, validateRoutePricingPublishInput } from '../commerce/validation.js';
import { resolveCommerceVersion } from '../commerce/versions.js';
import { transaction } from '../database.js';

function hashSecret(secret) {
  return createHash('sha256').update(secret).digest();
}

export async function applyDeclarativeSeed(pool, {
  appName,
  appDescription,
  redirectUris,
  operatorUserId,
  region,
  currency,
  creditsPerCurrency,
  personalCards = [],
  enterpriseCards = [],
  routes = [],
}) {
  return transaction(pool, async (connection) => {
    let { rows: [app] } = await connection.query(
      'SELECT id, name, description, redirect_uris AS "redirectUris", enabled FROM apps WHERE name = $1',
      [appName],
    );
    if (!app) {
      const secret = randomBytes(32).toString('base64url');
      const { rows: [inserted] } = await connection.query(`
        INSERT INTO apps (id, name, description, client_secret_hash, redirect_uris, enabled)
        VALUES ($1, $2, $3, $4, $5, true)
        RETURNING id, name, description, redirect_uris AS "redirectUris", enabled`,
      [randomUUID(), appName, appDescription, hashSecret(secret), redirectUris]);
      app = inserted;
    }

    for (const [accountKind, specs] of [['personal', personalCards], ['enterprise', enterpriseCards]]) {
      if (specs.length === 0) continue;
      const currentVersion = await resolveCommerceVersion(connection, {
        appId: app.id, accountKind, region, at: new Date(),
      });
      const snapshotByTier = new Map((currentVersion?.cardsSnapshot ?? []).map((c) => [c.tierId, c]));

      for (const spec of specs) {
        const parsed = validateDraftInput({ accountKind, ...spec });
        if (!parsed) throw new Error(`Invalid seed product card: ${appName}/${accountKind}/${spec.tierId}`);

        const { rows: [draft] } = await connection.query(`
          INSERT INTO product_card_drafts (
            id, app_id, account_kind, region, currency, product_type,
            tier_id, tier_rank, publish_status, rule_json, display_json, updated_by
          ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'published', $9::jsonb, $10::jsonb, $11)
          ON CONFLICT (app_id, account_kind, region, tier_id) DO UPDATE
            SET tier_rank = EXCLUDED.tier_rank,
                publish_status = 'published',
                rule_json = EXCLUDED.rule_json,
                display_json = EXCLUDED.display_json,
                updated_by = EXCLUDED.updated_by,
                updated_at = statement_timestamp()
          RETURNING *`, [
          randomUUID(), app.id, accountKind, region, currency, parsed.productType,
          parsed.tierId, parsed.tierRank, JSON.stringify(parsed.rule), JSON.stringify(parsed.display), operatorUserId,
        ]);

        snapshotByTier.set(draft.tier_id, {
          cardId: draft.id,
          productType: draft.product_type,
          tierId: draft.tier_id,
          tierRank: draft.tier_rank,
          rule: draft.rule_json,
          display: draft.display_json,
        });
      }

      const cardsSnapshot = [...snapshotByTier.values()].sort(
        (a, b) => (a.tierRank - b.tierRank) || a.cardId.localeCompare(b.cardId),
      );
      const shortApp = app.id.replaceAll('-', '').slice(0, 4);
      const k = accountKind === 'personal' ? 'p' : 'e';
      await connection.query(`
        INSERT INTO commerce_versions (
          id, version_code, app_id, account_kind, region, currency,
          effective_at, cards_snapshot, change_summary, published_by
        ) VALUES ($1, $2, $3, $4, $5, $6, statement_timestamp(), $7::jsonb, $8, $9)`, [
        randomUUID(),
        `ver_${shortApp}_${region}_${k}_${randomBytes(3).toString('hex')}`,
        app.id,
        accountKind,
        region,
        currency,
        JSON.stringify(cardsSnapshot),
        `Seed ${appName} ${accountKind} (${cardsSnapshot.length} cards)`,
        operatorUserId,
      ]);
    }

    if (routes.length > 0) {
      for (const accountKind of ['personal', 'enterprise']) {
        const parsed = validateRoutePricingPublishInput({
          accountKind,
          creditsPerCurrency,
          routes,
        });
        if (parsed.error) throw new Error(`Invalid seed route pricing for ${appName}: ${parsed.error} ${parsed.routeId ?? ''}`);
        const shortApp = app.id.replaceAll('-', '').slice(0, 4);
        const k = accountKind === 'personal' ? 'p' : 'e';
        await connection.query(`
          INSERT INTO route_pricing_versions (
            id, version_code, app_id, account_kind, region, currency,
            effective_at, credits_per_currency, fx_version, routes_snapshot, published_by
          ) VALUES ($1, $2, $3, $4, $5, $6, statement_timestamp(), $7, $8, $9::jsonb, $10)`, [
          randomUUID(),
          `rate_${shortApp}_${region}_${k}_${randomBytes(3).toString('hex')}`,
          app.id,
          accountKind,
          region,
          currency,
          parsed.value.creditsPerCurrency,
          parsed.value.fxVersion,
          JSON.stringify(parsed.value.routes),
          operatorUserId,
        ]);
      }
    }

    return { app, region, currency };
  });
}
