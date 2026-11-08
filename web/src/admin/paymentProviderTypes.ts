// Public merchant DTOs mirror internal/paymentconfig/types.go. Credentials are write-only.
export type PaymentProvider = {
 id: 'wechat' | 'alipay'; enabled: boolean; configured: boolean;
 app_id: string; merchant_id: string; seller_id: string; certificate_serial: string;
 platform_key_ids: string[]; notify_url: string; return_url: string;
 private_key_configured: boolean; api_v3_key_configured: boolean;
 public_key_configured: boolean; platform_keys_configured: boolean; missing_fields: string[];
};
export type PaymentProviderStatus = {
 version: number; mode: 'direct' | 'sandbox' | 'disabled'; managed: boolean;
 store_ready: boolean; configured: boolean; providers: [PaymentProvider, PaymentProvider];
};
export type PaymentProviderUpdate = {
 expected_version: number; reason: string; enabled: boolean;
 app_id?: string; merchant_id?: string; seller_id?: string; certificate_serial?: string;
 platform_key_ids?: string[]; private_key_pem?: string; api_v3_key?: string;
 public_key_pem?: string; platform_public_keys?: Record<string, string>;
};
