# Native metering (`metered_v2`)

UniHub supports explicit native units alongside the existing `exclusive_v1` token contract. A `Rate` with `semantics: "metered_v2"` carries a `metered` price; its final `Usage` carries only `metered` usage. Legacy counters and native measurements cannot be mixed. Existing rates retain their original semantics.

Supported operations are `chat`, `responses`, `embedding`, `speech`, `transcription`, `image_generation`, `image_edit`, `ocr` and `video`. Units are `token`, `character`, `audio_second`, `image`, `request`, `video_second`, and `provider_unit`. Supplier units require an explicit namespace. OCR page units are rejected in this version.

Quantities are canonical nonnegative integer strings with a decimal scale from 0 to 9, for example `{"value":"1001","scale":3}` means 1.001 seconds. Token, character, image and request counts require scale 0. Money remains in the selected asset's micro-units; CNY/6 and CREDIT/6 are not converted or summed into one balance.

Example native price, with a deliberately synthetic fee of two asset micro-units per audio second:

```json
{
  "version": "metered_v2",
  "operation": "transcription",
  "policy": "provider_reported_v1",
  "terms": [
    {"key":"audio","unit":"audio_second","price":{"numerator":"2","denominator":"1"}}
  ]
}
```

The corresponding `usage.metered` receipt is:

```json
{
  "version": "metered_v2",
  "operation": "transcription",
  "state": "confirmed",
  "source": "provider",
  "measurements": [
    {"key":"audio","unit":"audio_second","quantity":{"value":"1001","scale":3}}
  ]
}
```

UniHub aggregates the rational charge exactly and rounds up once at the asset boundary. In this example the final charge is 3 micro-units, subject to the frozen hold and existing platform-difference handling. A positive minimum or step applies only to a positive quantity; confirmed zero still charges zero.

Reservation freezes the complete rate, asset, payer, origin product and funding allocations. Settlement uses that snapshot even if the published rate later changes. Replaying the same final command is idempotent; changing its usage conflicts. Missing, invalid or incomplete observations do not become estimates. Native receipt summaries preserve dimensions and use string quantities; financial access checks remain in force.

LLMRouter supplies measurement evidence, chooses only implemented provider policies, and manages bounded reservations/async observation. UniHub is the platform funding authority. A model credential cannot make LLMRouter debit its shared local principal. A platform billing configuration or price does not certify an upstream provider contract.

`TestNativeMeteredUnitsAndAssetsHTTPAcceptance` exercises chat, embedding, speech, transcription, image, OCR and both video units through real HTTP with isolated PostgreSQL and Formance state, for both CNY/6 and CREDIT/6. This is accounting protocol acceptance, not live supplier or real-payment acceptance. Formal provider/model pricing remains an explicit operator deployment decision.
