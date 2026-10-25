-- Historical model credentials are CREDIT capabilities. Funding metadata is
-- immutable per capability and cannot be selected by a reservation request.
ALTER TABLE model_credentials
 ADD COLUMN funding_mode text NOT NULL DEFAULT 'credits' CHECK(funding_mode IN ('credits','prepaid_cash')),
 ADD COLUMN asset text NOT NULL DEFAULT 'CREDIT/6' CHECK(asset IN ('CREDIT/6','CNY/6')),
 ADD COLUMN billing_profile_version bigint NOT NULL DEFAULT 0 CHECK(billing_profile_version >= 0),
 ADD CONSTRAINT model_credentials_funding_asset CHECK((funding_mode='credits' AND asset='CREDIT/6') OR (funding_mode='prepaid_cash' AND asset='CNY/6'));
