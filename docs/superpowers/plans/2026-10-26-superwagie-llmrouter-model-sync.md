# SuperWagie LLMRouter Model Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Inventory every model actually used by the installed SuperWagie/OpenCode runtime, built-in skills, and embedded knowledge library; synchronize the chat-capable catalog into the local LLMRouter Gateway model and ability tables; and identify operation-specific models that require dedicated LLMRouter/UniHub adapters before billing.

**Architecture:** Read model IDs from the installed SuperWagie OpenCode configuration/runtime, the model-purpose catalog consumed by built-in skills, and the embedded knowledge provisioning/defaults code. Update the local LLMRouter SQLite Gateway channel and enabled abilities idempotently for the generic chat catalog, while recording OCR, embedding, rerank, speech, image, and video models as operation-specific routes until their protocol and pricing adapters exist. Run provider-level smoke requests using credentials only in memory. UniHub platform billing remains a separate gate; this preparation does not publish rates or enable production platform traffic.

**Tech Stack:** Python 3 standard library, SQLite, OpenCode runtime API, LLMRouter local SQLite database.

**Spec:** `/Users/neomei/项目/codexprojects/SuperWagie-open/docs/superpowers/specs/2026-10-25-quukk-unified-multimodal-billing.md`; `/Users/neomei/项目/codexprojects/SuperWagie-open/docs/superpowers/specs/2026-10-23-unified-model-configuration.md`

## Global Constraints

- Preserve existing local LLMRouter database rows and create a timestamped backup before mutation.
- Never write provider credentials, tokens, passwords, or access-bearing URLs into source, plans, reports, or command output.
- Use the installed SuperWagie `quukk` model IDs as the initial platform-facing chat catalog; include active OpenCode/skill models in the inventory, but keep provider-specific non-chat operations distinct until their platform route and pricing contract are mapped.
- Do not claim real LLMRouter/UniHub settlement from direct provider smoke tests; record the evidence boundary explicitly.

### Task 1: Capture and validate the installed model catalog and consumers

**Files:**
- Read: `/Users/neomei/.config/opencode/opencode.json`
- Read: `http://127.0.0.1:62972/api/model`
- Read: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/model-configuration/catalog.js`
- Read: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/embedded-knowledge/local-defaults.js`
- Read: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/embedded-knowledge/provision.js`
- Read: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/embedded-knowledge/model-bindings.js`
- Write: `/tmp/superwagie-llmrouter-model-probe.json`

- [ ] Extract the 22 `quukk` model IDs and verify the running SuperWagie API exposes the same set.
- [ ] Extract active model-purpose presets used by built-in skills and classify each as generic chat, operation-specific adapter, or planned/unimplemented.
- [ ] Extract embedded knowledge defaults for embedding, rerank, vision, OCR, ASR, and importable models without exporting secrets; probe the local backend health and capability endpoints.
- [ ] Probe the configured Quukk endpoint with a minimal request per model using the existing credential only in memory.
- [ ] Probe the separately configured DashScope model with one minimal real request.

### Task 2: Synchronize the local LLMRouter Gateway catalog

**Files:**
- Modify: `/Users/neomei/项目/codexprojects/llmrouter/one-api.db`
- Backup: `/Users/neomei/项目/codexprojects/llmrouter/one-api.db.pre-superwagie-model-sync-<timestamp>`

- [ ] Update the existing Gateway channel model CSV to contain the union of its existing models and the 22 `quukk` IDs.
- [ ] Insert or update enabled `default` abilities for channel 1 for every synchronized model.
- [ ] Leave provider credentials and UniHub rate configuration untouched.

### Task 3: Record the skill and knowledge route map

**Files:**
- Write: `/Users/neomei/项目/codexprojects/unihub/.local/superwagie-llmrouter-model-sync-20261026.json`

- [ ] Record every discovered model with provider, purpose, protocol, status, and route classification.
- [ ] Mark embedding/rerank/OCR/vision/speech/image/video models requiring operation-specific adapters instead of placing them in the generic chat ability list.
- [ ] Record local knowledge backend health/capability evidence and the existing model identity/index compatibility constraint.

### Task 4: Verify and report

**Files:**
- [ ] Re-read the database and verify exact model/ability equality and no duplicate abilities.
- [ ] Run focused LLMRouter tests covering channel ability/model selection.
- [ ] Record direct provider smoke results, database mutation counts, skill/knowledge route coverage, and the distinction between catalog readiness and UniHub-billed platform readiness.
