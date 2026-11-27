import { registerCatalogRoutes } from './catalog.js';
import { registerProductDraftRoutes } from './drafts.js';
import { registerRoutePricingRoutes } from './routes-pricing.js';
import { registerVersionRoutes } from './versions.js';

export function registerCommerceRoutes(app, pool, auth, write, isPlatformOperator, runtime) {
  registerProductDraftRoutes(app, pool, auth, write, isPlatformOperator, runtime);
  registerRoutePricingRoutes(app, pool, auth, write, isPlatformOperator, runtime);
  registerVersionRoutes(app, pool, auth, isPlatformOperator, runtime);
  registerCatalogRoutes(app, pool, auth, runtime);
}
