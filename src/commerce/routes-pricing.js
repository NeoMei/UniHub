import { randomBytes, randomUUID } from 'node:crypto';
import { isUuid } from '../database.js';
import { isValidAccountKind, parseEffectiveAt, validateRoutePricingPublishInput } from './validation.js';
import { canManageApp, resolveRoutePricingVersion } from './versions.js';

function makeRateVersionCode(appId, region, accountKind) {
  const shortApp = appId.replaceAll('-', '').slice(0, 4);
  const k = accountKind === 'personal' ? 'p' : 'e';
  return `rate_${shortApp}_${region}_${k}_${randomBytes(3).toString('hex')}`;
}

export function registerRoutePricingRoutes(app, pool, auth, write, isPlatformOperator, { region, currency }) {
  const writeOpts = { ...write, bodyLimit: 65_536 };

  app.get('/api/admin/apps/:appId/route-pricing', auth, async (request, reply) => {
    const { appId } = request.params;
    const { kind = 'personal', at } = request.query ?? {};
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    if (!isValidAccountKind(kind)) return reply.code(400).send({ error: 'invalid_account_kind' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }
    const atDate = parseEffectiveAt(at);
    if (!atDate) return reply.code(400).send({ error: 'invalid_effective_at' });

    const activeVersion = await resolveRoutePricingVersion(pool, {
      appId, accountKind: kind, region, at: atDate,
    });
    return { appId, accountKind: kind, region, currency, activeVersion };
  });

  app.post('/api/admin/apps/:appId/route-pricing/publish', writeOpts, async (request, reply) => {
    const { appId } = request.params;
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    if (!(await canManageApp(pool, request.auth, appId, isPlatformOperator))) {
      return reply.code(403).send({ error: 'app_forbidden' });
    }

    const parsed = validateRoutePricingPublishInput(request.body);
    if (parsed.error) {
      return reply.code(400).send(
        parsed.routeId ? { error: parsed.error, routeId: parsed.routeId } : { error: parsed.error },
      );
    }

    const { accountKind, creditsPerCurrency, fxVersion, effectiveAt, routes } = parsed.value;
    const id = randomUUID();
    const versionCode = makeRateVersionCode(appId, region, accountKind);

    try {
      const { rows: [version] } = await pool.query(`
        INSERT INTO route_pricing_versions (
          id, version_code, app_id, account_kind, region, currency,
          effective_at, credits_per_currency, fx_version, routes_snapshot, published_by
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
        RETURNING id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
          region, currency, effective_at AS "effectiveAt", credits_per_currency AS "creditsPerCurrency",
          fx_version AS "fxVersion", routes_snapshot AS "routesSnapshot",
          published_by AS "publishedBy", published_at AS "publishedAt"`,
      [
        id, versionCode, appId, accountKind, region, currency,
        effectiveAt, creditsPerCurrency, fxVersion, JSON.stringify(routes), request.auth.userId,
      ]);
      return reply.code(201).send({ version });
    } catch (error) {
      if (error.code === '23505') return reply.code(409).send({ error: 'effective_at_conflict' });
      if (error.code === '23503') return reply.code(404).send({ error: 'app_not_found' });
      throw error;
    }
  });
}
