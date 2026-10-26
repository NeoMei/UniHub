export type Profile = { id:string; username:string; display_name:string; avatar_url?:string; email:string; version:string };
export type Session = {
  authenticated: boolean;
  user?: { id: string; name: string; email: string };
  csrf_token?: string;
  sandbox: boolean;
};
export type Account = { id: string; kind: string; name: string; role: string };
export type BillingProfile = {
  product_id: string;
  version: number;
  subscription: { enabled: boolean };
  included_credits: { enabled: boolean };
  addons: { enabled: boolean };
  usage: {
    enabled: boolean;
    require_subscription: boolean;
    funding_modes: string[];
    default_funding_mode: string;
  };
};
export type Product = {
  id: string;
  name: string;
  billing_profile?: BillingProfile;
};
export type Wallet = { product_id: string; asset: string; amount: string };
export type Member = {
  user_id: string;
  name: string;
  email: string;
  role: string;
};
export type Operation = {
  id: string;
  account_id: string;
  kind: string;
  status: string;
  created_at: string;
  error_code?: string;
};
export type Recharge = {
  id: string;
  account_id: string;
  product_id: string;
  amount: string;
  operation_id?: string;
};
export type Offer = {
  id: string;
  product_id: string;
  name: string;
  account_kind: string;
  kind: string;
  period: string;
  price: string;
  monthly_credit: string;
  entitlements: Record<string, string>;
  rank: number;
  validity_months: number;
  refund_rule?: string;
  status: string;
  sandbox: boolean;
};
export type Subscription = {
  id: string;
  account_id: string;
  product_id: string;
  offer: Offer;
  version: number;
  anchor: string;
  paid_through: string;
  status: string;
};
export type Order = {
  id: string;
  account_id: string;
  product_id: string;
  kind: string;
  offer: Offer;
  amount: string;
  product_funding: string;
  unified_funding: string;
  payment_status: string;
  fulfillment_status: string;
  start: string;
  end: string;
  period_start: string;
  period_end: string;
  grant_amount: string;
  created_at: string;
  refund_status?: string;
  refund_reason?: string;
};
export type Quote = {
  id: string;
  account_id: string;
  subscription_id: string;
  subscription_version: number;
  offer: Offer;
  amount: string;
  grant_amount: string;
  period_start: string;
  expires_at: string;
};
export type Batch = {
  id: string;
  account_id: string;
  product_id: string;
  order_id: string;
  subscription_id?: string;
  kind: string;
  original: string;
  available: string;
  reserved: string;
  consumed: string;
  expired: string;
  withdrawn: string;
  starts_at: string;
  expires_at: string;
  frozen: boolean;
};
export type Authorization = {
  product_id: string;
  purpose: string;
  enabled: boolean;
  version: number;
  max_per_operation: string;
  max_total: string;
  used: string;
  expires_at: string;
};
