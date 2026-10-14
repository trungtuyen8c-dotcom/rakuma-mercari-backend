-- Why an order left the approval queue, so nothing is skipped silently: when, by whom, which kind and the reason.
-- dismiss_kind: manual (owner pressed Bỏ qua), row_deleted (its purchase row was deleted), refund_removed (row removed
-- after a full refund), unknown (skipped before this history existed and not found in the audit log).
ALTER TABLE rakuma_orders ADD COLUMN dismissed_at TIMESTAMPTZ;
ALTER TABLE rakuma_orders ADD COLUMN dismissed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE rakuma_orders ADD COLUMN dismiss_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE rakuma_orders ADD COLUMN dismiss_reason TEXT NOT NULL DEFAULT '';

-- Backfill the orders already skipped from the audit log: the latest Bỏ qua press or deletion of a row with its link
WITH events AS (
  SELECT o.id, a.at, a.actor, 'manual' AS kind, 'Bấm Bỏ qua' AS reason
  FROM rakuma_orders o
  JOIN audit_logs a ON a.entity = 'rakuma_order' AND a.entity_id = o.id::text AND a.action = 'dismiss'
                   AND (a.after ->> 'dismissed') = 'true'
  WHERE o.is_dismissed
  UNION ALL
  SELECT o.id, a.at, a.actor,
         CASE WHEN a.after ? 'refundId' THEN 'refund_removed' ELSE 'row_deleted' END,
         CASE WHEN a.after ? 'refundId' THEN 'Hoàn tiền đủ và xoá dòng nhập' ELSE 'Đã xoá dòng nhập' END
           || ' #' || a.entity_id || ' (kỳ ' || COALESCE(a.before ->> 'periodLabel', '?') || ' dòng ' || COALESCE(a.before ->> 'stt', '?')
           || ', ' || COALESCE(a.before ->> 'total', '?') || '¥)'
  FROM rakuma_orders o
  JOIN audit_logs a ON a.entity = 'purchase' AND a.action = 'delete' AND (a.before ->> 'link') = o.item_url
  WHERE o.is_dismissed
), latest AS (
  SELECT DISTINCT ON (id) * FROM events ORDER BY id, at DESC
)
UPDATE rakuma_orders o SET dismissed_at = l.at, dismissed_by = l.actor, dismiss_kind = l.kind, dismiss_reason = l.reason
FROM latest l WHERE l.id = o.id;

UPDATE rakuma_orders SET dismiss_kind = 'unknown', dismiss_reason = 'Bỏ qua trước khi có lịch sử, không tìm thấy trong nhật ký'
WHERE is_dismissed AND dismiss_kind = '';
