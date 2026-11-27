import { isUuid } from '../database.js';
import { isValidAccountKind, parseEffectiveAt } from './validation.js';

export async function canManageApp(pool, session, appId, isPlatformOperator) {
  if (isPlatformOperator(session)) return true;
  const { rowCount } = await pool.query(
    'SELECT 1 FROM app_operators WHERE app_id = $1 AND user_id = $2',
    [appId, session.userId],
  );
  return rowCount === 1;
}

export async function resolveCommerceVersion(pool, { appId, accountKind, region, at = new Date() }) {
  const { rows: [version] } = await pool.query(`
    SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
      region, currency, effective_at AS "effectiveAt", cards_snapshot AS "cardsSnapshot",
      change_summary AS "changeSummary", published_by AS "publishedBy", published_at AS "publishedAt"
    FROM commerce_versions
    WHERE app_id = $1 AND account_kind = $2 AND region = $3 AND effective_at <= $4
    ORDER BY effective_at DESC
    LIMIT 1`, [appId, accountKind, region, at]);
  return version ?? null;
}

export async function resolveRoutePricingVersion(pool, { appId, accountKind, region, at = new Date() }) {
  const { rows: [version] } = await pool.query(`
    SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
      region, currency, effective_at AS "effectiveAt", credits_per_currency AS "creditsPerCurrency",
      fx_version AS "fxVersion", routes_snapshot AS "routesSnapshot",
      published_by AS "publishedBy", published_at AS "publishedAt"
    FROM route_pricing_versions
    WHERE app_id = $1 AND account_kind = $2 AND region = $3 AND effective_at <= $4
    ORDER BY effective_at DESC
    LIMIT 1`, [appId, accountKind, region, at]);
  return version ?? null;
}

export function registerVersionRoutes(app, pool, auth, isPlatformOperator, { region }) {
  // List historical immutable versions for an app (supports header "历史版本 (N)" modal)
  app.get('/api/admin/apps/:appId/versions', auth, async (request, reply) => {
    const { appId } = request.params;
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const type = request.query?.type ?? 'all';
    const kind = request.query?.kind;
    if (kind && !isValidAccountKind(kind)) return reply.code(400).send({ error: 'invalid_account_kind' });

    const versions = [];
    if (type === 'all' || type === 'commerce') {
      const { rows } = await pool.query(`
        SELECT id, version_code AS "versionCode", 'commerce' AS "versionType", app_id AS "appId",
          account_kind AS "accountKind", region, currency, effective_at AS "effectiveAt",
          change_summary AS "changeSummary", jsonb_array_length(cards_snapshot) AS "itemCount",
          published_at AS "publishedAt"
        FROM commerce_versions
        WHERE app_id = $1 AND region = $2 AND ($3::text IS NULL OR account_kind = $3)
        ORDER BY effective_at DESC, published_at DESC LIMIT 100`,
      [appId, region, kind ?? null]);
      versions.push(...rows);
    }
    if (type === 'all' || type === 'pricing') {
      const { rows } = await pool.query(`
        SELECT id, version_code AS "versionCode", 'pricing' AS "versionType", app_id AS "appId",
          account_kind AS "accountKind", region, currency, effective_at AS "effectiveAt",
          ('模型路由与售价（' || jsonb_array_length(routes_snapshot) || ' 条路由）') AS "changeSummary",
          jsonb_array_length(routes_snapshot) AS "itemCount",
          published_at AS "publishedAt"
        FROM route_pricing_versions
        WHERE app_id = $1 AND region = $2 AND ($3::text IS NULL OR account_kind = $3)
        ORDER BY effective_at DESC, published_at DESC LIMIT 100`,
      [appId, region, kind ?? null]);
      versions.push(...rows);
    }
    versions.sort((a, b) => new Date(b.effectiveAt) - new Date(a.effectiveAt));
    return { versions };
  });

  // Read immutable commerce snapshot by ID
  app.get('/api/commerce/versions/:versionId', auth, async (request, reply) => {
    const { versionId } = request.params;
    if (!isUuid(versionId)) return reply.code(400).send({ error: 'invalid_version' });
    const { rows: [version] } = await pool.query(`
      SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
        region, currency, effective_at AS "effectiveAt", cards_snapshot AS "cardsSnapshot",
        change_summary AS "changeSummary", published_by AS "publishedBy", published_at AS "publishedAt"
      FROM commerce_versions WHERE id = $1 AND region = $2`, [versionId, region]);
    if (!version) return reply.code(404).send({ error: 'version_not_found' });
    if (!(await canManageApp(pool, request.auth, version.appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    return { version };
  });

  // Read immutable route-pricing snapshot by ID
  app.get('/api/pricing/versions/:versionId', auth, async (request, reply) => {
    const { versionId } = request.params;
    if (!isUuid(versionId)) return reply.code(400).send({ error: 'invalid_version' });
    const { rows: [version] } = await pool.query(`
      SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
        region, currency, effective_at AS "effectiveAt", credits_per_currency AS "creditsPerCurrency",
        fx_version AS "fxVersion", routes_snapshot AS "routesSnapshot",
        published_by AS "publishedBy", published_at AS "publishedAt"
      FROM route_pricing_versions WHERE id = $1 AND region = $2`, [versionId, region]);
    if (!version) return reply.code(404).send({ error: 'version_not_found' });
    if (!(await canManageApp(pool, request.auth, version.appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    return { version };
  });

  // Customer storefront: resolve live published cards for active payer account kind + region + time
  app.get('/api/storefront/products', auth, async (request, reply) => {
    const { appId, at } = request.query ?? {};
    const atDate = parseEffectiveAt(at);
    if (!atDate) return reply.code(400).send({ error: 'invalid_effective_at' });
    if (appId && !isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });

    const { rows: [activeAccount] } = await pool.query(
      'SELECT id, kind FROM accounts WHERE id = $1', [request.auth.activeAccountId],
    );
    if (!activeAccount) return reply.code(403).send({ error: 'account_forbidden' });

    const { rows: apps } = await pool.query(
      'SELECT id, name, description FROM apps WHERE enabled = true AND ($1::uuid IS NULL OR id = $1) ORDER BY name, id',
      [appId ?? null],
    );
    const storefronts = [];
    for (const targetApp of apps) {
      const version = await resolveCommerceVersion(pool, {
        appId: targetApp.id,
        accountKind: activeAccount.kind,
        region,
        at: atDate,
      });
      if (!version) continue;
      storefronts.push({
        app: targetApp,
        versionId: version.id,
        versionCode: version.versionCode,
        accountKind: version.accountKind,
        region: version.region,
        currency: version.currency,
        effectiveAt: version.effectiveAt,
        cards: version.cardsSnapshot,
      });
    }
    return { accountKind: activeAccount.kind, region, storefronts };
  });
}
