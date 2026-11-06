package billing

// ReportCallsSQL normalizes the two billing channels for read-only reporting.
// Internal IDs are namespaced for cursor ordering; query_call_id keeps managed
// call addresses compatible and gives runtime calls a product/instance-safe ID.
// Product-native usage is deliberately absent: its arbitrary JSON is neither
// the managed usage schema nor a safe customer-facing projection.
const ReportCallsSQL = `
 SELECT 'managed_model:'||c.id AS id,c.account_id,c.created_at,c.time_source,
 (CASE WHEN c.snapshot#>>'{native_pricing,pricing_version}'='lr-native-v1' AND NOT COALESCE(` + validLegacyUsageSQL + `,false) THEN c.snapshot-'usage' ELSE c.snapshot END)||jsonb_build_object(
   'query_call_id',c.id,'source','managed_model','source_call_id',c.id,
   'price_owner',CASE WHEN c.snapshot#>>'{native_pricing,pricing_version}'='lr-native-v1' THEN 'product' ELSE 'platform' END,
   'price_version',COALESCE(c.snapshot#>>'{native_pricing,pricing_version}',c.snapshot#>>'{rate,id}',''),
   'operation',COALESCE(c.snapshot->>'wire_operation',''),'refunded','0',
   'waiver',(SELECT jsonb_build_object('case_id',w.case_id,'provider_state',w.provider_state,
      'customer_status',CASE WHEN release.status='APPLIED' AND c.snapshot->>'status'='RELEASED' THEN 'RELEASED' WHEN release.status='UNKNOWN' THEN 'UNKNOWN' ELSE 'PENDING' END)
     FROM billing_platform_fault_waivers w JOIN billing_commands release ON release.id=w.release_command_id
     WHERE w.call_id=c.id AND w.account_id=c.account_id)) AS snapshot,
 CASE WHEN EXISTS(SELECT 1 FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.status='UNKNOWN' AND bc.kind IN ('reserve','topup','settle','release') AND bc.effect#>>'{call,call_id}'=c.id) THEN 'UNKNOWN' ELSE COALESCE(c.snapshot->>'status','UNKNOWN') END AS report_status
 FROM billing_calls c
 UNION ALL
 SELECT 'product_runtime:'||c.id,c.account_id,evidence.created_at,
 CASE WHEN evidence.created_at IS NULL THEN 'unknown' ELSE 'runtime_reserve_command' END,
 jsonb_build_object(
   'query_call_id','product_runtime:'||c.id,'source','product_runtime','source_call_id',c.snapshot->>'call_id',
   'actor_id',c.snapshot->>'actor_id','origin_product_id',c.snapshot->>'product_id',
   'instance_id',c.snapshot->>'instance_id','operation',c.snapshot->>'operation',
   'charge_asset',c.snapshot->>'asset','rate',jsonb_build_object('model',c.snapshot->>'model'),
   'funding_mode',CASE WHEN c.snapshot->>'asset'='CNY/6' THEN 'prepaid_cash' ELSE 'product_quota' END,
   'funding_product_id',CASE WHEN c.snapshot->>'asset'='CNY/6' THEN '' ELSE c.snapshot->>'product_id' END,
   'status',c.snapshot->>'status','held',c.snapshot->>'held','charged',c.snapshot->>'charged','refunded',COALESCE(c.snapshot->>'refunded','0'),
   'price_version',c.snapshot->>'price_version','price_owner','product'),
 CASE WHEN EXISTS(SELECT 1 FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.status='UNKNOWN' AND bc.kind IN ('runtime_reserve','runtime_topup','runtime_settle','runtime_release','runtime_refund') AND bc.effect#>>'{runtime_call,id}'=c.id) THEN 'UNKNOWN' ELSE COALESCE(c.snapshot->>'status','UNKNOWN') END
 FROM product_runtime_calls c
 LEFT JOIN LATERAL (SELECT min(bc.created_at) AS created_at FROM billing_commands bc
   WHERE bc.account_id=c.account_id AND bc.kind='runtime_reserve' AND bc.effect#>>'{runtime_call,id}'=c.id) evidence ON true
`
