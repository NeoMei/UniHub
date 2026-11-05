-- Private first-observation evidence for valid settlement reports exceeding
-- the serialized call hold. Separate from snapshots and all ledger commands:
-- subsequent recovery/extension/settlement/release writes must retain it.
-- RuntimeSettle bounds usage to 64 KiB before serialization; this ceiling also
-- accommodates JSONB whitespace normalization and bounded call metadata.
ALTER TABLE product_runtime_calls ADD COLUMN rejected_settlement jsonb
 CHECK (rejected_settlement IS NULL OR
   (jsonb_typeof(rejected_settlement)='object' AND
    octet_length(rejected_settlement::text)<=262144));
