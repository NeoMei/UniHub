import { randomBytes, randomUUID } from 'node:crypto';
import { isUuid, transaction } from '../database.js';
import { isValidAccountKind, parseEffectiveAt, validateDraftInput } from './validation.js';
import { canManageApp, resolveCommerceVersion } from './versions.js';

function makeVersionCode(prefix, appId, region, accountKind) {
  const shortApp = appId.replaceAll('-', '').slice(0, 4);
  const k = accountKind === 'personal' ? 'p' : 'e';
  const suffix = randomBytes(3).toString('hex');
  return `${prefix}_${shortApp}_${region}_${k}_${suffix}`;
}

function toCardSnapshot(draft) {
  return {
    cardId: draft.id,
    productType: draft.product_type,
    tierId: draft.tier_id,
    tierRank: draft.tier_rank,
    rule: draft.rule_json,
    display: draft.display_json,
  };
}

function sortCardsSnapshot(cards) {
  return [...cards].sort((a, b) => (a.tierRank - b.tierRank) || a.cardId.localeCompare(b.cardId));
}

function mapDraftRow(row) {
  return {
    id: row.id,
    appId: row.app_id,
    accountKind: row.account_kind,
    region: row.region,
    currency: row.currency,
    productType: row.product_type,
    tierId: row.tier_id,
    tierRank: row.tier_rank,
    publishStatus: row.publish_status,
    rule: row.rule_json,
    display: row.display_json,
    updatedBy: row.updated_by,
    updatedAt: row.updated_at,
  };
}

async function publishOrUnpublishSingleCard(pool, {
  appId, cardId, userId, region, currency, mode, effectiveAt,
}) {
  return transaction(pool, async (connection) => {
    const { rows: [target] } = await connection.query(`
      SELECT * FROM product_card_drafts
      WHERE id = $1 AND app_id = $2 AND region = $3
      FOR UPDATE`, [cardId, appId, region]);
    if (!target) return { status: 404, error: 'card_not_found' };

    // Lock all drafts in same scope so concurrent single-card publish/unpublish serialize cleanly
    await connection.query(`
      SELECT id FROM product_card_drafts
      WHERE app_id = $1 AND account_kind = $2 AND region = $3
      ORDER BY id FOR UPDATE`, [appId, target.account_kind, region]);

    const currentVersion = await resolveCommerceVersion(connection, {
      appId,
      accountKind: target.account_kind,
      region,
      at: effectiveAt,
    });
    const baseCards = Array.isArray(currentVersion?.cardsSnapshot) ? currentVersion.cardsSnapshot : [];
    const filtered = baseCards.filter((c) => c.cardId !== target.id && c.tierId !== target.tier_id);
    const nextCards = mode === 'publish'
      ? sortCardsSnapshot([...filtered, toCardSnapshot(target)])
      : sortCardsSnapshot(filtered);

    const versionId = randomUUID();
    const versionCode = makeVersionCode('ver', appId, region, target.account_kind);
    const cardName = target.display_json?.name ?? target.tier_id;
    const changeSummary = mode === 'publish'
      ? `单卡上架「${cardName}」（线上共 ${nextCards.length} 张已上架卡）`
      : `单卡下架「${cardName}」（线上剩余 ${nextCards.length} 张已上架卡）`;

    let version;
    try {
      const { rows: [created] } = await connection.query(`
        INSERT INTO commerce_versions (
          id, version_code, app_id, account_kind, region, currency,
          effective_at, cards_snapshot, change_summary, published_by
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
        RETURNING id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
          region, currency, effective_at AS "effectiveAt", cards_snapshot AS "cardsSnapshot",
          change_summary AS "changeSummary", published_by AS "publishedBy", published_at AS "publishedAt"`,
      [
        versionId, versionCode, appId, target.account_kind, region, currency,
        effectiveAt, JSON.stringify(nextCards), changeSummary, userId,
      ]);
      version = created;
    } catch (error) {
      if (error.code === '23505') return { status: 409, error: 'effective_at_conflict' };
      throw error;
    }

    const nextStatus = mode === 'publish' ? 'published' : 'offline';
    const { rows: [updatedDraft] } = await connection.query(`
      UPDATE product_card_drafts
      SET publish_status = $1, updated_by = $2, updated_at = statement_timestamp()
      WHERE id = $3 RETURNING *`, [nextStatus, userId, target.id]);

    return { status: 201, version, card: mapDraftRow(updatedDraft) };
  });
}

export function registerProductDraftRoutes(app, pool, auth, write, isPlatformOperator, { region, currency }) {
  const writeOpts = { ...write, bodyLimit: 65_536 };

  app.get('/api/admin/apps/:appId/product-drafts', auth, async (request, reply) => {
    const { appId } = request.params;
    const { kind = 'personal', status } = request.query ?? {};
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    if (!isValidAccountKind(kind)) return reply.code(400).send({ error: 'invalid_account_kind' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }

    const { rows } = await pool.query(`
      SELECT * FROM product_card_drafts
      WHERE app_id = $1 AND account_kind = $2 AND region = $3
      ORDER BY tier_rank ASC, id ASC`, [appId, kind, region]);

    const cards = rows.map(mapDraftRow).filter((c) => {
      if (status === 'published') return c.publishStatus === 'published';
      if (status === 'draft') return c.publishStatus !== 'published';
      return true;
    });
    const activeVersion = await resolveCommerceVersion(pool, { appId, accountKind: kind, region });
    return {
      appId,
      accountKind: kind,
      region,
      currency,
      activeVersion: activeVersion
        ? { id: activeVersion.id, versionCode: activeVersion.versionCode, effectiveAt: activeVersion.effectiveAt, liveCardCount: activeVersion.cardsSnapshot.length }
        : null,
      cards,
    };
  });

  app.post('/api/admin/apps/:appId/product-drafts', writeOpts, async (request, reply) => {
    const { appId } = request.params;
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const input = validateDraftInput(request.body);
    if (!input) return reply.code(400).send({ error: 'invalid_product_card' });

    try {
      const { rows: [created] } = await pool.query(`
        INSERT INTO product_card_drafts (
          id, app_id, account_kind, region, currency, product_type,
          tier_id, tier_rank, publish_status, rule_json, display_json, updated_by
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'new', $9::jsonb, $10::jsonb, $11)
        RETURNING *`, [
        randomUUID(), appId, input.accountKind, region, currency, input.productType,
        input.tierId, input.tierRank, JSON.stringify(input.rule), JSON.stringify(input.display), request.auth.userId,
      ]);
      return reply.code(201).send({ card: mapDraftRow(created) });
    } catch (error) {
      if (error.code === '23505') return reply.code(409).send({ error: 'duplicate_tier_id' });
      if (error.code === '23503') return reply.code(404).send({ error: 'app_not_found' });
      throw error;
    }
  });

  app.put('/api/admin/apps/:appId/product-drafts/:cardId', writeOpts, async (request, reply) => {
    const { appId, cardId } = request.params;
    if (!isUuid(appId) || !isUuid(cardId)) return reply.code(400).send({ error: 'invalid_request' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const input = validateDraftInput(request.body);
    if (!input) return reply.code(400).send({ error: 'invalid_product_card' });

    try {
      const { rows: [updated] } = await pool.query(`
        UPDATE product_card_drafts
        SET product_type = $1,
            tier_id = $2,
            tier_rank = $3,
            rule_json = $4::jsonb,
            display_json = $5::jsonb,
            publish_status = CASE WHEN publish_status = 'published' THEN 'modified' ELSE publish_status END,
            updated_by = $6,
            updated_at = statement_timestamp()
        WHERE id = $7 AND app_id = $8 AND account_kind = $9 AND region = $10
        RETURNING *`, [
        input.productType, input.tierId, input.tierRank,
        JSON.stringify(input.rule), JSON.stringify(input.display),
        request.auth.userId, cardId, appId, input.accountKind, region,
      ]);
      if (!updated) return reply.code(404).send({ error: 'card_not_found' });
      return { card: mapDraftRow(updated) };
    } catch (error) {
      if (error.code === '23505') return reply.code(409).send({ error: 'duplicate_tier_id' });
      throw error;
    }
  });

  app.delete('/api/admin/apps/:appId/product-drafts/:cardId', writeOpts, async (request, reply) => {
    const { appId, cardId } = request.params;
    if (!isUuid(appId) || !isUuid(cardId)) return reply.code(400).send({ error: 'invalid_request' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const forceUnpublish = request.query?.forceUnpublish === 'true';

    const result = await transaction(pool, async (connection) => {
      const { rows: [existing] } = await connection.query(
        'SELECT * FROM product_card_drafts WHERE id = $1 AND app_id = $2 AND region = $3 FOR UPDATE',
        [cardId, appId, region],
      );
      if (!existing) return { status: 404, error: 'card_not_found' };
      if ((existing.publish_status === 'published' || existing.publish_status === 'modified') && !forceUnpublish) {
        return { status: 409, error: 'card_currently_published' };
      }
      if (existing.publish_status === 'published' || existing.publish_status === 'modified') {
        const currentVersion = await resolveCommerceVersion(connection, {
          appId, accountKind: existing.account_kind, region, at: new Date(),
        });
        if (currentVersion) {
          const remaining = currentVersion.cardsSnapshot.filter((c) => c.cardId !== existing.id);
          await connection.query(`
            INSERT INTO commerce_versions (
              id, version_code, app_id, account_kind, region, currency,
              effective_at, cards_snapshot, change_summary, published_by
            ) VALUES ($1, $2, $3, $4, $5, $6, statement_timestamp(), $7::jsonb, $8, $9)`, [
            randomUUID(), makeVersionCode('ver', appId, region, existing.account_kind),
            appId, existing.account_kind, region, currency,
            JSON.stringify(remaining), `删除并下架「${existing.display_json?.name ?? existing.tier_id}」`, request.auth.userId,
          ]);
        }
      }
      await connection.query('DELETE FROM product_card_drafts WHERE id = $1', [cardId]);
      return { status: 204 };
    });

    if (result.status === 204) return reply.code(204).send();
    return reply.code(result.status).send({ error: result.error });
  });

  app.post('/api/admin/apps/:appId/product-drafts/:cardId/publish', writeOpts, async (request, reply) => {
    const { appId, cardId } = request.params;
    if (!isUuid(appId) || !isUuid(cardId)) return reply.code(400).send({ error: 'invalid_request' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const effectiveAt = parseEffectiveAt(request.body?.effectiveAt);
    if (!effectiveAt) return reply.code(400).send({ error: 'invalid_effective_at' });

    const result = await publishOrUnpublishSingleCard(pool, {
      appId, cardId, userId: request.auth.userId, region, currency, mode: 'publish', effectiveAt,
    });
    if (result.error) return reply.code(result.status).send({ error: result.error });
    return reply.code(201).send({ version: result.version, card: result.card });
  });

  app.post('/api/admin/apps/:appId/product-drafts/:cardId/unpublish', writeOpts, async (request, reply) => {
    const { appId, cardId } = request.params;
    if (!isUuid(appId) || !isUuid(cardId)) return reply.code(400).send({ error: 'invalid_request' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const effectiveAt = parseEffectiveAt(request.body?.effectiveAt);
    if (!effectiveAt) return reply.code(400).send({ error: 'invalid_effective_at' });

    const result = await publishOrUnpublishSingleCard(pool, {
      appId, cardId, userId: request.auth.userId, region, currency, mode: 'unpublish', effectiveAt,
    });
    if (result.error) return reply.code(result.status).send({ error: result.error });
    return reply.code(201).send({ version: result.version, card: result.card });
  });
}
