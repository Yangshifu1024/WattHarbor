import { Table } from 'antd';
import { useEffect, useState } from 'react';
import { http, type ApiEnvelope } from '../../api/client';
import { LoadError } from '../../components/LoadError';

interface Refund {
  refund_no: string;
  refund_cents: number;
  status: string;
  reason: string;
  created_at: string;
}

const statuses: Record<string, string> = {
  pending: '待处理', processing: '处理中', succeeded: '成功', failed: '失败', rejected: '已驳回',
};

export default function OrderRefunds({ orderID }: { orderID: number }) {
  const [items, setItems] = useState<Refund[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(undefined);
    http.get<ApiEnvelope<{ items: Refund[] }>>(`/api/v1/admin/orders/${orderID}/refunds`, { signal: controller.signal })
      .then(({ data }) => { if (!controller.signal.aborted) setItems(data.data?.items ?? []); })
      .catch(() => { if (!controller.signal.aborted) setError('退款记录暂时无法读取'); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [orderID, reload]);

  if (error) return <LoadError title={error} onRetry={() => setReload(value => value + 1)} />;
  return <Table rowKey="refund_no" loading={loading} dataSource={items} scroll={{ x: 480 }}
    locale={{ emptyText: '暂无退款记录' }}
    columns={[
      { title: '退款单号', dataIndex: 'refund_no' },
      { title: '金额', dataIndex: 'refund_cents', render: (cents: number) => `¥${(cents / 100).toFixed(2)}` },
      { title: '状态', dataIndex: 'status', render: (status: string) => statuses[status] ?? status },
      { title: '原因', dataIndex: 'reason' },
      { title: '申请时间', dataIndex: 'created_at' },
    ]}
  />;
}
