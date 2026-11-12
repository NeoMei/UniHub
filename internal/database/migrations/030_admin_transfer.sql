-- A conserved cross-account transfer debits the operator's own personal
-- unified wallet instead of minting from world. It is a distinct wallet
-- operation kind so the worker's recharge/transfer-specific gates do not apply.
ALTER TABLE wallet_operations DROP CONSTRAINT IF EXISTS wallet_operations_kind_check;
ALTER TABLE wallet_operations ADD CONSTRAINT wallet_operations_kind_check CHECK (kind IN ('transfer','recharge','admin_transfer'));
