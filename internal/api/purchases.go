package api

import (
	"hoanxu/internal/affiliate"
	"hoanxu/internal/platform"
	"net/http"
)

// Each reported order appears once; links without reports appear independently.
// Legacy attribution uses link_id; signed reports use the owner's tracking token.
const purchasesSQL = `WITH entries AS (
 SELECT md5('link:'||l.id::text)::uuid AS id,l.created_at AS sort_at,
 CASE WHEN l.tracking_sub_ids IS NULL THEN 'legacy' WHEN l.expires_at<=now() OR l.lifecycle_status='cancelled' THEN 'rejected' ELSE 'selecting' END AS status,
 'link'::text AS kind,l.id AS link_id,NULL::uuid AS order_id
 FROM affiliate_links l WHERE l.user_id=$1 AND NOT EXISTS(
 SELECT 1 FROM orders o WHERE o.user_id=$1 AND (o.link_id=l.id OR o.tracking_code=l.tracking_code))
 UNION ALL
 SELECT md5('order:'||o.id::text)::uuid,o.ordered_at,
 CASE o.status WHEN 'pending' THEN 'progress' WHEN 'approved' THEN 'completed' ELSE 'rejected' END,
 'order',linked.id,o.id
 FROM orders o LEFT JOIN LATERAL (
 SELECT l.id FROM affiliate_links l WHERE l.user_id=$1 AND (o.link_id=l.id OR o.tracking_code=l.tracking_code)
 ORDER BY (o.link_id=l.id) DESC NULLS LAST,l.id LIMIT 1
 ) linked ON true WHERE o.user_id=$1
)
SELECT jsonb_build_object('id',p.id,'kind',p.kind,'status',p.status,'sortAt',p.sort_at,
'link',(SELECT ` + affiliate.LinkJSON + ` ` + affiliate.LinkFromSQL + ` WHERE l.user_id=$1 AND l.id=p.link_id),
'order',(` + orderSQL + ` WHERE o.user_id=$1 AND o.id=p.order_id))
FROM entries p WHERE ($4='all' OR p.status=$4) ORDER BY p.sort_at DESC,p.id DESC LIMIT $2 OFFSET $3`

func (s *Server) purchases(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "all"
	}
	switch status {
	case "selecting", "progress", "completed", "rejected", "all":
	default:
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_STATUS", "Trạng thái không hợp lệ."))
		return
	}
	limit, offset := page(r)
	s.list(w, r, purchasesSQL, user(r).ID, limit, offset, status)
}
