-- Explicit native-credit keys are a new contract. Never rewrite existing keys.
ALTER TABLE model_credentials DROP CONSTRAINT model_credentials_scope_contract;
ALTER TABLE model_credentials ADD CONSTRAINT model_credentials_scope_contract CHECK (
 credential_version > 0 AND (
  (model_scope='selected' AND pricing_source='') OR
  (model_scope='llmrouter_available' AND credential_kind='api_key' AND origin_product_id='superwagie' AND (
   (pricing_source='llmrouter_native' AND funding_mode='prepaid_cash' AND asset='CNY/6') OR
   (pricing_source='llmrouter_native_credit' AND funding_mode='credits' AND asset='CREDIT/6')
  ))
 )
);
