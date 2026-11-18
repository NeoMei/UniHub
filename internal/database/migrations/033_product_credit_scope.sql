-- Historical credentials, calls and batches remain in the shared credit pool.
ALTER TABLE model_credentials ADD COLUMN credit_scope text NOT NULL DEFAULT '';
ALTER TABLE model_credentials DROP CONSTRAINT model_credentials_scope_contract;
ALTER TABLE model_credentials ADD CONSTRAINT model_credentials_scope_contract CHECK (
 credential_version > 0 AND (
  (credit_scope='' AND model_scope='selected' AND pricing_source='') OR
  (model_scope='llmrouter_available' AND credential_kind='api_key' AND (
   (credit_scope='' AND origin_product_id='superwagie' AND pricing_source='llmrouter_native' AND funding_mode='prepaid_cash' AND asset='CNY/6') OR
   ((credit_scope='product_v1' OR (credit_scope='' AND origin_product_id='superwagie'))
    AND pricing_source='llmrouter_native_credit' AND funding_mode='credits' AND asset='CREDIT/6')
  ))
 )
);
CREATE INDEX billing_batches_product_credit ON billing_batches (account_id, (snapshot->>'product_id'))
 WHERE snapshot->>'credit_scope'='product_v1';
