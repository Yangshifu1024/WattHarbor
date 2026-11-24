import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Layout, Menu, Avatar, Button, Dropdown, Typography, type MenuProps } from 'antd';
import {
  DashboardOutlined, AlertOutlined, GiftOutlined, AccountBookOutlined,
  SettingOutlined, UserOutlined, LogoutOutlined, ThunderboltOutlined, SafetyCertificateOutlined,
  ShoppingCartOutlined, PayCircleOutlined, EnvironmentOutlined, ToolOutlined, LayoutOutlined,
  TeamOutlined, CreditCardOutlined, TagOutlined, NotificationOutlined,
  FundOutlined, DownloadOutlined, ShopOutlined, WarningOutlined, CustomerServiceOutlined,
  UserSwitchOutlined, AuditOutlined, MonitorOutlined,
} from '@ant-design/icons';
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { apiGet, apiPost, adminSession } from '../api/client';
import type { SessionProfile } from '../api/session';

const { Header, Content } = Layout;
const { Text } = Typography;

type NavLink = { key: string; icon?: ReactNode; label: string; permission: string };
type NavSection = { key: string; icon: ReactNode; label: string; children: NavLink[] };

const dashboard: NavLink = { key: '/', icon: <DashboardOutlined />, label: '仪表盘', permission: 'dashboard.read' };
const sections: NavSection[] = [
  { key: 'charge', icon: <ThunderboltOutlined />, label: '充电运营', children: [
    { key: '/orders', icon: <ShoppingCartOutlined />, label: '充电订单', permission: 'order.read' },
    { key: '/payment-orders', icon: <PayCircleOutlined />, label: '支付订单', permission: 'order.read' },
    { key: '/stations', icon: <EnvironmentOutlined />, label: '站点', permission: 'station.read' },
    { key: '/devices', icon: <ToolOutlined />, label: '设备', permission: 'device.read' },
    { key: '/templates', icon: <LayoutOutlined />, label: '模板', permission: 'pricing.read' },
  ] },
  { key: 'users', icon: <GiftOutlined />, label: '用户运营', children: [
    { key: '/users', icon: <TeamOutlined />, label: '充电用户', permission: 'user.read' },
    { key: '/online-cards', icon: <CreditCardOutlined />, label: '在线卡', permission: 'user.read' },
    { key: '/coupons', icon: <TagOutlined />, label: '优惠券', permission: 'coupon.read' },
    { key: '/announcements', icon: <NotificationOutlined />, label: '公告', permission: 'announcement.read' },
  ] },
  { key: 'finance', icon: <AccountBookOutlined />, label: '财务管理', children: [
    { key: '/billing', icon: <FundOutlined />, label: '财务', permission: 'finance.read' },
    { key: '/exports', icon: <DownloadOutlined />, label: '数据导出', permission: 'finance.read' },
  ] },
  { key: 'device-ops', icon: <AlertOutlined />, label: '设备运维', children: [
    { key: '/vendors', icon: <ShopOutlined />, label: '厂商', permission: 'vendor.read' },
    { key: '/alerts', icon: <WarningOutlined />, label: '告警', permission: 'alert.read' },
    { key: '/casework', icon: <CustomerServiceOutlined />, label: '反馈报修', permission: 'feedback.read' },
  ] },
  { key: 'system', icon: <SettingOutlined />, label: '系统管理', children: [
    { key: '/admin-users', icon: <UserSwitchOutlined />, label: '管理员', permission: 'admin_user.read' },
    { key: '/audit-logs', icon: <AuditOutlined />, label: '审计日志', permission: 'audit.read' },
    { key: '/diagnostics', icon: <MonitorOutlined />, label: '运行诊断', permission: 'audit.read' },
  ] },
];
const links = [dashboard, ...sections.flatMap(section => section.children)];

export default function MainLayout() {
  const [, sessionChanged] = useState(0);
  useEffect(() => { const update = () => sessionChanged(v => v + 1); window.addEventListener('cp-session', update); window.addEventListener('storage', update); return () => { window.removeEventListener('cp-session', update); window.removeEventListener('storage', update); }; }, []);
  const sessionEpoch = adminSession.epoch();
  useEffect(() => {
    // 进入会话时同步身份；登录和续期响应已经包含最新权限，无需按窗口焦点重复查询。
    void adminSession.syncProfile(() => apiGet<SessionProfile>('/api/v1/admin/auth/me')).catch(() => undefined);
  }, [sessionEpoch]);
  const navigate = useNavigate();
  const { pathname } = useLocation();

  const onLogout = async () => {
    const epoch = adminSession.epoch();
    await apiPost('/api/v1/admin/auth/logout').catch(() => undefined);
    if (await adminSession.clear(epoch)) navigate('/login', { replace: true });
  };

  const adminInfo = (() => {
    try { return JSON.parse(localStorage.getItem('cp_admin') || 'null'); } catch { return null; }
  })();
  const permissions = new Set<string>(Array.isArray(adminInfo?.permissions) ? adminInfo.permissions : []);
  const visibleSections = sections.map(section => ({ ...section, children: section.children.filter(link => permissions.has(link.permission)) }))
    .filter(section => section.children.length > 0);
  const selectedLink = links.find(link => pathname === link.key || link.key !== '/' && pathname.startsWith(link.key + '/'));
  const pageTitle = pathname === '/security' ? '账号安全' : selectedLink?.label;
  const activeSection = visibleSections.find(section => section.children.some(link => link.key === selectedLink?.key));
  const primaryItems: MenuProps['items'] = [
    ...(permissions.has(dashboard.permission) ? [{ key: dashboard.key, icon: dashboard.icon, label: dashboard.label }] : []),
    ...visibleSections.map(section => ({ key: section.key, icon: section.icon, label: section.label })),
  ];
  const primarySelected = activeSection?.key ?? (selectedLink?.key === dashboard.key && permissions.has(dashboard.permission) ? dashboard.key : '');
  const secondaryItems: MenuProps['items'] = (activeSection?.children ?? []).map(link => ({ key: link.key, icon: link.icon, label: link.label }));

  const openPrimary = (key: string) => {
    if (key === dashboard.key) {
      navigate(dashboard.key);
      return;
    }
    const section = visibleSections.find(item => item.key === key);
    if (!section || section.children.length === 0) return;
    if (section.key === activeSection?.key) return;
    navigate(section.children[0].key);
  };

  const headerRef = useRef<HTMLDivElement>(null);
  const subRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const header = headerRef.current;
    const sub = subRef.current;
    if (!header || !sub) return;
    const align = () => {
      const selected = header.querySelector<HTMLElement>('.layout-navigation-primary .ant-menu-item-selected');
      const first = sub.querySelector<HTMLElement>('.ant-menu-item');
      if (!selected || !first) return;
      const pad = Number.parseFloat(sub.style.paddingLeft || '0') || 0;
      const next = Math.max(0, pad + selected.getBoundingClientRect().left - first.getBoundingClientRect().left);
      if (Math.abs(next - pad) > 0.5) sub.style.paddingLeft = `${Math.round(next)}px`;
    };
    align();
    const observer = new ResizeObserver(align);
    observer.observe(header);
    return () => observer.disconnect();
  }, [primarySelected, pathname]);

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header className="layout-header" style={{ height: 'auto', lineHeight: 'normal', padding: 0 }}>
        <div className="layout-header-frame" ref={headerRef}>
          <div className="layout-header-top">
            <div className="layout-heading">
              <Text strong className="layout-brand">WattHarbor ·</Text>
              {pageTitle && <Typography.Title level={3} className="layout-page-title">{pageTitle}</Typography.Title>}
            </div>
            <Menu
              mode="horizontal"
              className="layout-navigation layout-navigation-primary"
              aria-label="后台导航"
              selectedKeys={primarySelected ? [primarySelected] : []}
              items={primaryItems}
              onClick={({ key }) => openPrimary(key)}
            />
            <Dropdown
              trigger={['click']}
              menu={{
                items: [
                  { key: 'security', icon: <SafetyCertificateOutlined />, label: '账号安全', onClick: () => navigate('/security') },
                  { key: 'logout', icon: <LogoutOutlined />, label: '退出', onClick: onLogout },
                ],
              }}
            >
              <Button type="text" aria-label="账号菜单" style={{ height: 'auto', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
                <Avatar icon={<UserOutlined />} />
                <span>{adminInfo?.display_name?.trim() || adminInfo?.username || 'admin'}</span>
                <Text type="secondary" style={{ fontSize: 12 }}>{adminInfo?.role_name || adminInfo?.role || ''}</Text>
              </Button>
            </Dropdown>
          </div>
          {secondaryItems.length > 0 && (
            <div className="layout-header-sub" ref={subRef}>
              <Menu
                mode="horizontal"
                disabledOverflow
                className="layout-navigation layout-navigation-secondary"
                aria-label={activeSection ? `${activeSection.label}子菜单` : '子菜单'}
                selectedKeys={selectedLink && permissions.has(selectedLink.permission) ? [selectedLink.key] : []}
                items={secondaryItems}
                onClick={({ key }) => navigate(key)}
              />
            </div>
          )}
        </div>
      </Header>
      <Content>
        <Outlet />
      </Content>
    </Layout>
  );
}
