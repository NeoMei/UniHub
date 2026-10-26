# UniHub 产品站 Design QA

- source visual truth: `/Users/neomei/.codex/generated_images/01a176e5-c04a-7f82-a24e-f76288289aed/exec-cff52cb7-9fb1-42e3-8f4e-fe1d93b1cca9.png`
- implementation: `http://127.0.0.1:5175/` served from the built `web/dist` through a local session-only preview
- viewport: 1280 × 960 CSS px, desktop, unauthenticated sandbox state
- source dimensions: 1505 × 1045 px; implementation browser capture: 1280 × 960 px; no density normalization was needed for the browser view
- state: product portal before login; primary links point to `/auth/login`; anchor links target product, workflow, and settings-boundary sections

## Comparison evidence

The source concept and implementation were inspected in the same QA pass. The implementation was opened in the Codex IAB browser and checked at the first viewport and after scrolling to the capability, workflow, and boundary sections.

- First viewport: both use a restrained white/pale-blue page, compact product navigation, split hero copy plus console preview, blue/teal CTA treatment, and a clear next-section transition.
- Typography: implementation uses the existing UniHub system stack with explicit heading/control sizes; Chinese headings retain the large, dense hierarchy from the concept.
- Spacing and rhythm: hero uses a two-column layout, generous section padding, four capability cards, four workflow columns, and a separate boundary band.
- Colors and tokens: implementation uses UniHub teal plus blue accents, pale blue-gray background bands, white surfaces, thin borders, and soft shadows.
- Image and asset fidelity: existing UniHub logo assets are reused; the console preview is code-native so its controls remain accessible and links remain functional.
- Copy: portal copy describes UniHub identity, accounts, products, billing, quotas, and settings boundaries; no AgentWiki claims are reused.

## Interactions verified

- `产品能力`, `使用方式`, and `设置边界` anchor links are present and navigate to the matching sections.
- Header `登录归一（UniHub）`, `进入控制台`, hero `进入控制台`, and final CTA all point to `/auth/login`.
- Existing authenticated console tests continue to pass alongside the new portal test.

## Findings

No actionable P0, P1, or P2 differences were found in the inspected desktop state. The concept includes illustrative product rows; the implementation renders those rows as accessible HTML so they remain readable and testable.

## Follow-up polish

Mobile CSS collapses the portal navigation, hero, capability cards, workflow steps, and boundary section into a single column. A dedicated 390px browser capture can be added in a later visual pass.

final result: passed
