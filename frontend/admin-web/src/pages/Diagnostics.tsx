import { useCallback, useEffect, useState } from 'react';
import { Alert, Button, Card, Space, Table, Tag } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../api/client';
import { LoadError } from '../components/LoadError';

interface Scanner {
  state: 'running' | 'failed' | 'ok';
  started_at: string;
  last_success: string | null;
  duration_ms: number;
}

interface Queue {
  count: number;
  oldest_at: string | null;
}

interface Diagnostics {
  scanners: Record<string, Scanner>;
  queues: Record<string, Queue>;
  unavailable: string[];
}

const statuses = {
  running: { color: 'processing', label: '执行中' },
  ok: { color: 'success', label: '成功' },
  failed: { color: 'error', label: '失败' },
};
const queueNames: Record<string, string> = {
  domain_events_pending: '待发布业务事件',
  domain_events_failed: '发布失败业务事件',
  billing_pending: '待处理计费任务',
  charging_over_2h: '充电超过两小时的订单',
  exports_processing: '正在处理的导出任务',
  start_commands_sent: '待确认启动命令',
  stop_commands_unacked: '待确认停止命令',
};
const formatTime = (value: string | null) => value ? new Date(value).toLocaleString() : '—';

export default function DiagnosticsPage() {
  const [data, setData] = useState<Diagnostics>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setData(await apiGet<Diagnostics>('/api/v1/admin/diagnostics'));
    } catch (err) {
      setData(undefined);
      setError(err instanceof Error ? err.message : '诊断读取失败');
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const scanners = Object.entries(data?.scanners ?? {}).sort(([a], [b]) => a.localeCompare(b))
    .map(([name, status]) => ({ name, ...status }));
  const queues = Object.entries(data?.queues ?? {}).map(([name, queue]) => ({ name, ...queue }));
  return (
    <Space direction="vertical" size="middle" className="page-container" style={{ display: 'flex' }}>
      <Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button>
      {error && <LoadError title="运行诊断加载失败" detail={error} onRetry={() => void load()} />}
      {!!data?.unavailable.length && <Alert showIcon type="warning"
        message="部分诊断暂不可用" description={data.unavailable.map(name => queueNames[name] ?? name).join('、')} />}
      <Card title="后台任务">
        <Table rowKey="name" dataSource={scanners} loading={loading} pagination={false}
          scroll={{ x: 650 }} columns={[
            { title: '任务', dataIndex: 'name' },
            { title: '状态', render: (_, row) => <Tag color={statuses[row.state].color}>{statuses[row.state].label}</Tag> },
            { title: '最近开始', dataIndex: 'started_at', render: formatTime },
            { title: '最近成功', dataIndex: 'last_success', render: formatTime },
            { title: '耗时', render: (_, row) => row.state === 'running' ? '—' : `${row.duration_ms} ms` },
          ]} />
      </Card>
      <Card title="待处理记录">
        <Table rowKey="name" dataSource={queues} loading={loading} pagination={false}
          scroll={{ x: 500 }} columns={[
            { title: '类型', render: (_, row) => queueNames[row.name] ?? row.name },
            { title: '数量', dataIndex: 'count' },
            { title: '最早时间', dataIndex: 'oldest_at', render: formatTime },
          ]} />
      </Card>
    </Space>
  );
}
