ALTER TABLE model_credentials
 ADD COLUMN model_scope text NOT NULL DEFAULT 'selected',
 ADD COLUMN pricing_source text NOT NULL DEFAULT '',
 ADD COLUMN credential_version bigint NOT NULL DEFAULT 1;
ALTER TABLE model_credentials ADD CONSTRAINT model_credentials_scope_contract CHECK (
 credential_version > 0 AND (
  (model_scope='selected' AND pricing_source='') OR
  (model_scope='llmrouter_available' AND pricing_source='llmrouter_native' AND credential_kind='api_key' AND origin_product_id='superwagie' AND funding_mode='prepaid_cash' AND asset='CNY/6')
 )
);
