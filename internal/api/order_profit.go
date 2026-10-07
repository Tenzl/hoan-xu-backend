package api

// Legacy import and manual history substitute cashback for an unknown commission.
// Never interpret these placeholder amounts as real revenue or tax.
const orderCommissionKnownSQL = `o.publisher NOT IN ('admin-legacy','legacy-server')`

// Integer division floors the fixed 5% tax to whole VND for each order.
const orderTaxSQL = `o.commission / 20`

// Only administrative routes use this projection. Customer routes keep orderSQL.
const adminOrderSQL = `SELECT ` + orderJSONSQL + ` || jsonb_build_object(
 'taxAmount',CASE WHEN o.status='rejected' THEN 0 WHEN ` + orderCommissionKnownSQL + ` THEN ` + orderTaxSQL + ` END,
 'projectedProfit',CASE WHEN o.status='rejected' THEN 0 WHEN ` + orderCommissionKnownSQL + ` THEN o.commission-o.cashback-(` + orderTaxSQL + `) END,
 'profitStatus',CASE WHEN o.status='rejected' THEN 'excluded' WHEN NOT (` + orderCommissionKnownSQL + `) THEN 'unavailable' WHEN o.status='pending' THEN 'estimated' ELSE 'projected' END
)` + orderFromSQL
